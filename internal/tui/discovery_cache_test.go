package tui

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func withDiscoveryCloneCache(t *testing.T, clone func(url, ref, dir string) error) string {
	t.Helper()
	oldRoot := discoveryCacheRoot
	oldClone := gitClone
	root := t.TempDir()
	discoveryCacheRoot = func() (string, error) { return root, nil }
	gitClone = clone
	t.Cleanup(func() {
		discoveryCacheRoot = oldRoot
		gitClone = oldClone
	})
	return root
}

func makeGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestCachedSourceCloneConcurrentCallsCloneOnce(t *testing.T) {
	var clones int32
	root := withDiscoveryCloneCache(t, func(url, ref, dir string) error {
		atomic.AddInt32(&clones, 1)
		time.Sleep(25 * time.Millisecond)
		makeGitRepo(t, dir)
		return nil
	})

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	dirs := make(chan string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir, cleanup, err := cachedSourceClone("https://github.com/owner/repo", "main", false)
			if err == nil {
				cleanup()
				dirs <- dir
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(dirs)

	for err := range errs {
		if err != nil {
			t.Fatalf("cachedSourceClone failed: %v", err)
		}
	}
	if got := atomic.LoadInt32(&clones); got != 1 {
		t.Fatalf("expected one clone, got %d", got)
	}
	want := filepath.Join(root, cloneCacheKey("https://github.com/owner/repo", "main"))
	for dir := range dirs {
		if dir != want {
			t.Fatalf("expected cached dir %q, got %q", want, dir)
		}
	}
}

func TestCachedSourceCloneForceFailurePreservesOldCache(t *testing.T) {
	root := withDiscoveryCloneCache(t, func(url, ref, dir string) error {
		return errors.New("clone failed")
	})
	target := filepath.Join(root, cloneCacheKey("https://github.com/owner/repo", "main"))
	makeGitRepo(t, target)

	if _, _, err := cachedSourceClone("https://github.com/owner/repo", "main", true); err == nil {
		t.Fatal("expected forced clone failure")
	}
	if !isGitRepo(target) {
		t.Fatalf("expected existing cache to remain valid at %q", target)
	}
}

func TestCachedSourceCloneMissFailureLeavesNoTargetDir(t *testing.T) {
	root := withDiscoveryCloneCache(t, func(url, ref, dir string) error {
		if err := os.WriteFile(filepath.Join(dir, "partial"), []byte("partial"), 0o644); err != nil {
			return err
		}
		return errors.New("clone failed")
	})
	target := filepath.Join(root, cloneCacheKey("https://github.com/owner/repo", "main"))

	if _, _, err := cachedSourceClone("https://github.com/owner/repo", "main", false); err == nil {
		t.Fatal("expected clone failure")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected no half-valid target dir, stat err=%v", err)
	}
}

func TestCachedSourceCloneForceReturnsTempWithoutDestroyingCache(t *testing.T) {
	root := withDiscoveryCloneCache(t, func(url, ref, dir string) error {
		makeGitRepo(t, dir)
		return os.WriteFile(filepath.Join(dir, "fresh"), []byte("fresh"), 0o644)
	})
	target := filepath.Join(root, cloneCacheKey("https://github.com/owner/repo", "main"))
	makeGitRepo(t, target)
	if err := os.WriteFile(filepath.Join(target, "old"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, cleanup, err := cachedSourceClone("https://github.com/owner/repo", "main", true)
	if err != nil {
		t.Fatalf("forced clone failed: %v", err)
	}
	if dir == target {
		t.Fatalf("expected force clone to return temp dir, got target %q", dir)
	}
	if !isGitRepo(target) {
		t.Fatalf("expected existing cache to remain valid at %q", target)
	}
	if _, err := os.Stat(filepath.Join(target, "old")); err != nil {
		t.Fatalf("expected old cache contents preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh")); err != nil {
		t.Fatalf("expected temp clone contents: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected cleanup to remove temp clone, stat err=%v", err)
	}
}

func TestCachedSourceCloneConcurrentForceCallsUseSeparateTempDirs(t *testing.T) {
	var clones int32
	withDiscoveryCloneCache(t, func(url, ref, dir string) error {
		atomic.AddInt32(&clones, 1)
		time.Sleep(25 * time.Millisecond)
		makeGitRepo(t, dir)
		return nil
	})

	const callers = 2
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	dirs := make(chan string, callers)
	cleanups := make(chan func(), callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir, cleanup, err := cachedSourceClone("https://github.com/owner/repo", "main", true)
			if err == nil {
				dirs <- dir
				cleanups <- cleanup
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(dirs)
	close(cleanups)

	for err := range errs {
		if err != nil {
			t.Fatalf("forced cachedSourceClone failed: %v", err)
		}
	}
	if got := atomic.LoadInt32(&clones); got != callers {
		t.Fatalf("expected %d independent force clones, got %d", callers, got)
	}
	seen := map[string]bool{}
	for dir := range dirs {
		if seen[dir] {
			t.Fatalf("force clone temp dir was shared: %q", dir)
		}
		seen[dir] = true
		if !isGitRepo(dir) {
			t.Fatalf("expected temp dir to remain valid before cleanup: %q", dir)
		}
	}
	for cleanup := range cleanups {
		cleanup()
	}
}
