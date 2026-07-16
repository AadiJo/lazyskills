package discovery

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alvinunreal/lazyskills/internal/model"
)

func TestDiscoverDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "one")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: One\ndescription: First skill\n---\n# One\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverDirectory(root)
	if err != nil || len(got) != 1 || got[0].Name != "One" || got[0].Description != "First skill" {
		t.Fatalf("DiscoverDirectory() = %#v, %v", got, err)
	}
}

func TestCachedSourceCloneSerializesMiss(t *testing.T) {
	root := t.TempDir()
	var clones atomic.Int32
	opts := Options{
		CacheRoot: func() (string, error) { return root, nil },
		Clone: func(_, _ string, dir string) error {
			clones.Add(1)
			time.Sleep(20 * time.Millisecond)
			return os.Mkdir(filepath.Join(dir, ".git"), 0o755)
		},
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cleanup, err := CachedSourceClone("https://github.com/owner/repo", "main", false, opts)
			if err != nil {
				t.Errorf("CachedSourceClone: %v", err)
				return
			}
			cleanup()
		}()
	}
	wg.Wait()
	if clones.Load() != 1 {
		t.Fatalf("expected one clone, got %d", clones.Load())
	}
}

func TestForcedCloneFailurePreservesCache(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, CloneCacheKey("https://github.com/owner/repo", "main"))
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := CachedSourceClone("https://github.com/owner/repo", "main", true, Options{
		CacheRoot: func() (string, error) { return root, nil },
		Clone:     func(_, _, _ string) error { return errors.New("clone failed") },
	})
	if err == nil || !IsGitRepo(target) {
		t.Fatalf("expected failure with preserved cache, err=%v", err)
	}
}

func TestParseRemoteGitHubSource(t *testing.T) {
	url, ref, ok := ParseRemoteGitHubSource("git+https://github.com/owner/repo.git#feature/ref")
	if !ok || url != "https://github.com/owner/repo" || ref != "feature/ref" {
		t.Fatalf("unexpected parse result %q %q %v", url, ref, ok)
	}
	for _, unsafe := range []string{"owner/repo/sub", "owner/repo#--bad", "https://gitlab.com/owner/repo"} {
		if _, _, ok := ParseRemoteGitHubSource(unsafe); ok {
			t.Fatalf("expected %q to be rejected", unsafe)
		}
	}
}

func TestResolveSourceRootRequiresWholePathComponents(t *testing.T) {
	valid := &model.Skill{CanonicalPath: filepath.Join(string(filepath.Separator), "tmp", "checkout", "skills", "foo"), LocalLock: &model.LocalLockEntry{SourceType: "directory", SkillPath: "skills/foo/SKILL.md"}}
	if got := ResolveSourceRoot(valid); got != filepath.Join(string(filepath.Separator), "tmp", "checkout") {
		t.Fatalf("valid source root = %q", got)
	}
	partial := &model.Skill{CanonicalPath: filepath.Join(string(filepath.Separator), "tmp", "evilskills", "foo"), LocalLock: &model.LocalLockEntry{SourceType: "directory", SkillPath: "skills/foo/SKILL.md"}}
	if got := ResolveSourceRoot(partial); got != "" {
		t.Fatalf("partial component match escaped to %q", got)
	}
}
