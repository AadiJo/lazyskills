package skillops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alvinunreal/lazyskills/internal/actions"
	"github.com/alvinunreal/lazyskills/internal/model"
	"github.com/alvinunreal/lazyskills/internal/runner"
)

func TestMoveShelfDisableEnable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "deploy")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	disabled := filepath.Join(root, ".lazyskills-disabled", "deploy")
	if got, partial := MoveShelf("disable_skill", []string{source}); got.ExitCode != 0 || partial {
		t.Fatalf("disable failed: %+v partial=%v", got, partial)
	}
	if got, partial := MoveShelf("enable_skill", []string{disabled, source}); got.ExitCode != 0 || partial {
		t.Fatalf("enable failed: %+v partial=%v", got, partial)
	}
}

func TestDeleteBrokenSymlinksRevalidates(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "broken")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Fatal(err)
	}
	result := DeleteBrokenSymlinks([]*model.Skill{{
		Name:  "broken",
		Scope: model.ScopeProject,
		ObservedPaths: []model.ObservedPath{{
			Path: link, Scope: model.ScopeProject, Status: model.StatusBrokenSymlink,
		}},
	}}, model.ScopeProject, "broken")
	if result.ExitCode != 0 {
		t.Fatalf("delete failed: %+v", result)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("expected symlink removal, err=%v", err)
	}
}

func TestRunBatchStopsAfterFailure(t *testing.T) {
	calls := 0
	result, partial := RunBatch("/tmp", []actions.ExecSpec{{Program: "one"}, {Program: "two"}, {Program: "three"}}, func(spec runner.ExecSpec) runner.Result {
		calls++
		if spec.Program == "two" {
			return runner.Result{Program: spec.Program, ExitCode: 1}
		}
		return runner.Result{Program: spec.Program, ExitCode: 0}
	})
	if calls != 2 || !partial || result.ExitCode != 1 {
		t.Fatalf("unexpected batch result calls=%d partial=%v result=%+v", calls, partial, result)
	}
}
