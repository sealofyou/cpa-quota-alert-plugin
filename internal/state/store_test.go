package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/monitor"
)

func TestLoadMissingInvalidAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	st, err := Load(missing)
	if err != nil || st.LowActive || len(st.PendingEvents) != 0 {
		t.Fatalf("missing load = %+v, %v", st, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "bad.json")); err == nil {
		t.Fatalf("expected non-object error")
	}
	if err := os.WriteFile(filepath.Join(dir, "compat.json"), []byte(`{"low_active":true,"future":"ignored"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = Load(filepath.Join(dir, "compat.json"))
	if err != nil || !st.LowActive {
		t.Fatalf("compat load = %+v, %v", st, err)
	}
}

func TestSaveAtomicAnd0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Save(path, monitor.State{LowActive: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600, got %v", info.Mode().Perm())
	}
	st, err := Load(path)
	if err != nil || !st.LowActive {
		t.Fatalf("Load saved: %+v %v", st, err)
	}
}

func TestSaveRenameFailureKeepsOldState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Save(path, monitor.State{LowActive: true}); err != nil {
		t.Fatal(err)
	}
	store := Store{Path: path, FS: renameFailFS{OSFS{}}}
	if err := store.Save(monitor.State{LowActive: false}); err == nil {
		t.Fatalf("expected rename failure")
	}
	st, err := Load(path)
	if err != nil || !st.LowActive {
		t.Fatalf("old state should remain intact: %+v %v", st, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".state.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files not cleaned: %v", matches)
	}
}

type renameFailFS struct{ OSFS }

func (renameFailFS) Rename(_, _ string) error { return errors.New("rename blocked") }
