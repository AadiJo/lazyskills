package locks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skills-lock.json")
	content := `{"version":1,"extra":"keep","skills":{"a":{"source":"o/r"},"b":{"source":"o/r2"}}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveEntry(path, "a"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}

	lock, err := ReadLocal(path)
	if err != nil {
		t.Fatalf("ReadLocal after prune: %v", err)
	}
	if _, gone := lock.Skills["a"]; gone {
		t.Error("expected entry 'a' to be removed")
	}
	if _, kept := lock.Skills["b"]; !kept {
		t.Error("expected entry 'b' to be preserved")
	}

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"extra"`) {
		t.Errorf("expected unknown top-level field to be preserved, got %s", raw)
	}

	if err := RemoveEntry(path, "missing"); err == nil {
		t.Error("expected an error removing a missing key")
	}
}
