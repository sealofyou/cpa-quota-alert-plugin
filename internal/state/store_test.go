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

func TestLoadMalformedJSONAndOpenFailure(t *testing.T) {
	dir := t.TempDir()
	malformed := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(malformed, []byte(`{"low_active":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(malformed); err == nil {
		t.Fatalf("expected malformed JSON error")
	}
	_, err := (Store{Path: filepath.Join(dir, "state.json"), FS: openFailFS{err: errors.New("permission denied")}}).Load()
	if err == nil {
		t.Fatalf("expected open failure")
	}
}

func TestSaveFailurePathsCleanupTempAndKeepOldState(t *testing.T) {
	cases := []struct {
		name string
		fs   func() FS
	}{
		{name: "temp-create", fs: func() FS { return faultFS{openFileErr: errors.New("create failed")} }},
		{name: "temp-chmod", fs: func() FS { return faultFS{fileFault: fileFault{chmodErr: errors.New("chmod failed")}} }},
		{name: "write", fs: func() FS { return faultFS{fileFault: fileFault{writeErr: errors.New("write failed")}} }},
		{name: "sync", fs: func() FS { return faultFS{fileFault: fileFault{syncErr: errors.New("sync failed")}} }},
		{name: "rename", fs: func() FS { return faultFS{renameErr: errors.New("rename failed")} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			if err := Save(path, monitor.State{LowActive: true}); err != nil {
				t.Fatal(err)
			}
			store := Store{Path: path, FS: tc.fs()}
			if err := store.Save(monitor.State{LowActive: false}); err == nil {
				t.Fatalf("expected save failure")
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
		})
	}
}

type openFailFS struct{ err error }

func (f openFailFS) Open(string) (File, error)                       { return nil, f.err }
func (f openFailFS) OpenFile(string, int, os.FileMode) (File, error) { return nil, f.err }
func (f openFailFS) Rename(string, string) error                     { return f.err }
func (f openFailFS) Remove(string) error                             { return f.err }
func (f openFailFS) Chmod(string, os.FileMode) error                 { return f.err }
func (f openFailFS) MkdirAll(string, os.FileMode) error              { return nil }

type faultFS struct {
	OSFS
	openFileErr error
	renameErr   error
	fileFault   fileFault
}

func (f faultFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	if f.openFileErr != nil {
		return nil, f.openFileErr
	}
	file, err := f.OSFS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return faultFile{File: file, fault: f.fileFault}, nil
}

func (f faultFS) Rename(oldpath, newpath string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.OSFS.Rename(oldpath, newpath)
}

type fileFault struct {
	writeErr error
	syncErr  error
	chmodErr error
}

type faultFile struct {
	File
	fault fileFault
}

func (f faultFile) Write(p []byte) (int, error) {
	if f.fault.writeErr != nil {
		return 0, f.fault.writeErr
	}
	return f.File.Write(p)
}

func (f faultFile) Sync() error {
	if f.fault.syncErr != nil {
		return f.fault.syncErr
	}
	return f.File.Sync()
}

func (f faultFile) Chmod(mode os.FileMode) error {
	if f.fault.chmodErr != nil {
		return f.fault.chmodErr
	}
	return f.File.Chmod(mode)
}
