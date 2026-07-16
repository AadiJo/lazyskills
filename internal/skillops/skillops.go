// Package skillops owns UI-agnostic skill mutations shared by the TUI and web UI.
package skillops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alvinunreal/lazyskills/internal/actions"
	"github.com/alvinunreal/lazyskills/internal/compat"
	"github.com/alvinunreal/lazyskills/internal/locks"
	"github.com/alvinunreal/lazyskills/internal/model"
	"github.com/alvinunreal/lazyskills/internal/runner"
)

// RunFunc executes one external command.
type RunFunc func(runner.ExecSpec) runner.Result

// MoveShelf performs an enable/disable shelf move after validating the entire
// plan. A partial move is reported when a later filesystem operation fails.
func MoveShelf(internal string, args []string) (result runner.Result, partial bool) {
	result = runner.Result{Program: internal, Args: append([]string{}, args...), ExitCode: -1}
	type move struct{ src, dest string }
	var plan []move
	seen := map[string]bool{}
	switch internal {
	case "disable_skill":
		for _, src := range args {
			if src == "" || seen[src] {
				continue
			}
			seen[src] = true
			plan = append(plan, move{src: src, dest: filepath.Join(filepath.Dir(src), ".lazyskills-disabled", filepath.Base(src))})
		}
	case "enable_skill":
		for i := 0; i+1 < len(args); i += 2 {
			if args[i] == "" || args[i+1] == "" || seen[args[i]] {
				continue
			}
			seen[args[i]] = true
			plan = append(plan, move{src: args[i], dest: args[i+1]})
		}
	default:
		result.Err = "unsupported shelf operation"
		return result, false
	}
	if len(plan) == 0 {
		result.Err = "shelf operation contains no valid paths"
		return result, false
	}
	var errs []string
	for _, item := range plan {
		if _, err := os.Lstat(item.src); err != nil {
			errs = append(errs, fmt.Sprintf("source path does not exist: %s", item.src))
			continue
		}
		if _, err := os.Lstat(item.dest); err == nil || !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("destination already exists: %s", item.dest))
			continue
		}
		if parent, err := os.Stat(filepath.Dir(item.dest)); err == nil && !parent.IsDir() {
			errs = append(errs, fmt.Sprintf("parent of destination is not a directory: %s", filepath.Dir(item.dest)))
		} else if err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("failed to check parent directory %s: %v", filepath.Dir(item.dest), err))
		}
	}
	if len(errs) > 0 {
		result.Err = compat.SanitizeMetadata(strings.Join(errs, "; "))
		return result, false
	}

	moved := 0
	for _, item := range plan {
		if err := os.MkdirAll(filepath.Dir(item.dest), 0o755); err != nil {
			errs = append(errs, fmt.Sprintf("failed to create directory %s: %v", filepath.Dir(item.dest), err))
			break
		}
		if err := os.Rename(item.src, item.dest); err != nil {
			errs = append(errs, fmt.Sprintf("failed to move %s to %s: %v", item.src, item.dest, err))
			break
		}
		moved++
	}
	if len(errs) > 0 {
		result.Err = compat.SanitizeMetadata(strings.Join(errs, "; "))
		return result, moved > 0
	}
	result.ExitCode = 0
	result.Stdout = fmt.Sprintf("%d skill path(s) moved", moved)
	return result, false
}

// PruneLock removes an orphaned lock entry for the requested scope.
func PruneLock(cwd, internal, key string) runner.Result {
	result := runner.Result{Program: "prune-lock", Args: []string{key}, ExitCode: -1}
	path := locks.ProjectLockPath(cwd)
	if internal == "prune_global_lock" {
		path = locks.GlobalLockPath()
	} else if internal != "prune_project_lock" {
		result.Err = "unsupported lock operation"
		return result
	}
	if err := locks.RemoveEntry(path, key); err != nil {
		result.Err = compat.SanitizeMetadata(err.Error())
		return result
	}
	result.ExitCode = 0
	result.Stdout = "lock entry pruned"
	return result
}

// DeleteBrokenSymlinks safely revalidates and removes only dangling symlinks
// matching the scoped skill identity.
func DeleteBrokenSymlinks(skills []*model.Skill, scope model.Scope, name string) runner.Result {
	result := runner.Result{Program: "delete-broken-symlink", Args: []string{name}, ExitCode: 0}
	removed, failed, firstErr := 0, 0, ""
	for _, skill := range skills {
		if skill == nil || skill.Name != name || skill.Scope != scope {
			continue
		}
		for _, observed := range skill.ObservedPaths {
			if observed.Status != model.StatusBrokenSymlink {
				continue
			}
			info, err := os.Lstat(observed.Path)
			if err != nil {
				if !os.IsNotExist(err) {
					failed++
					if firstErr == "" {
						firstErr = err.Error()
					}
				}
				continue
			}
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if _, err := os.Stat(observed.Path); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				failed++
				if firstErr == "" {
					firstErr = err.Error()
				}
				continue
			}
			if err := os.Remove(observed.Path); err != nil {
				failed++
				if firstErr == "" {
					firstErr = err.Error()
				}
				continue
			}
			removed++
		}
		break
	}
	if failed > 0 {
		result.ExitCode = -1
		result.Err = fmt.Sprintf("removed %d broken symlink(s), %d failed: %s", removed, failed, compat.SanitizeMetadata(firstErr))
		return result
	}
	result.Stdout = fmt.Sprintf("%d broken symlink(s) removed", removed)
	return result
}

// CleanupLockAfterRemove removes any lock entry left behind by a successful
// external remove command. The return value indicates a partial success.
func CleanupLockAfterRemove(action actions.CommandPreview, cwd string, result *runner.Result) bool {
	if action.ID != "remove" || result == nil || result.ExitCode != 0 || result.Err != "" || action.ConfirmValue == "" {
		return false
	}
	path := locks.ProjectLockPath(cwd)
	for _, arg := range action.Exec.Args {
		if arg == "-g" || arg == "--global" {
			path = locks.GlobalLockPath()
			break
		}
	}
	if _, err := locks.RemoveEntryIfExists(path, action.ConfirmValue); err != nil {
		result.ExitCode = -1
		result.Err = "removed skill, but failed to update lock: " + compat.SanitizeMetadata(err.Error())
		return true
	}
	return false
}

// RunBatch serially executes a command batch and stops at the first failure.
func RunBatch(cwd string, batch []actions.ExecSpec, run RunFunc) (runner.Result, bool) {
	if run == nil {
		run = runner.OSRunner{}.Run
	}
	lines := []string{}
	succeeded := 0
	for i, spec := range batch {
		result := run(runner.ExecSpec{Program: spec.Program, Args: spec.Args, Cwd: cwd})
		prefix := fmt.Sprintf("%d/%d %s", i+1, len(batch), compat.SanitizeMetadata(spec.Program))
		if result.ExitCode != 0 || result.Err != "" {
			result.Stdout = strings.Join(append(lines, prefix+" failed"), "\n")
			return result, succeeded > 0
		}
		succeeded++
		lines = append(lines, prefix+" ok")
	}
	return runner.Result{Program: "bulk", Cwd: cwd, ExitCode: 0, Stdout: strings.Join(lines, "\n")}, false
}

// ExecuteInternal dispatches a validated internal command preview.
func ExecuteInternal(action actions.CommandPreview, cwd string, snapshot model.ScanResult) (runner.Result, bool, bool) {
	switch action.Exec.Internal {
	case "enable_skill", "disable_skill":
		result, partial := MoveShelf(action.Exec.Internal, action.Exec.Args)
		return result, partial, true
	case "prune_project_lock", "prune_global_lock":
		return PruneLock(cwd, action.Exec.Internal, action.ConfirmValue), false, true
	case "delete_broken_symlink":
		if len(action.Exec.Args) < 2 || action.Exec.Args[0] == "" || action.Exec.Args[1] == "" {
			return runner.Result{Program: "delete-broken-symlink", ExitCode: -1, Err: "delete action is missing scoped skill identity"}, false, true
		}
		return DeleteBrokenSymlinks(snapshot.Skills, model.Scope(action.Exec.Args[0]), action.Exec.Args[1]), false, true
	default:
		return runner.Result{}, false, false
	}
}
