package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateStoreRoundTripAndDelete(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "private", "notification-secrets.json")}
	if err := s.Update(map[string]string{"SMTP_USER": "example-user", "SMTP_PASSWORD": "test-only-password"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Lookup("SMTP_PASSWORD"); !ok || got != "test-only-password" {
		t.Fatalf("lookup: found=%t", ok)
	}
	names, err := s.Names()
	if err != nil || len(names) != 2 || strings.Join(names, ",") != "SMTP_PASSWORD,SMTP_USER" {
		t.Fatalf("names=%v err=%v", names, err)
	}
	if err := s.Update(map[string]string{"SMTP_USER": "new-user"}, []string{"SMTP_PASSWORD"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup("SMTP_PASSWORD"); ok {
		t.Fatal("cleared secret still present")
	}
	if got, _ := s.Lookup("SMTP_USER"); got != "new-user" {
		t.Fatal("updated value not used")
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Stat(filepath.Dir(s.Path))
		if err != nil || dir.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode=%v err=%v", dir.Mode(), err)
		}
		file, err := os.Stat(s.Path)
		if err != nil || file.Mode().Perm() != 0o600 {
			t.Fatalf("file mode=%v err=%v", file.Mode(), err)
		}
	}
}

func TestDefaultUsesSystemdStateDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("systemd StateDirectory is Linux only")
	}
	private := filepath.Join(t.TempDir(), "state")
	t.Setenv("STATE_DIRECTORY", private)
	store, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if store.Path != filepath.Join(private, "notification-secrets.json") {
		t.Fatalf("path=%q", store.Path)
	}
}

func TestPrivateStoreRejectsInvalidAndExposedStorage(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "private", "notification-secrets.json")}
	for _, input := range []map[string]string{{"BAD-NAME": "value"}, {"GOOD": ""}, {"GOOD": "line\nbreak"}} {
		if err := s.Update(input, nil); err == nil {
			t.Fatalf("accepted invalid input %v", input)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(map[string]string{"SMTP_USER": "test"}, nil); err == nil {
		t.Fatal("accepted world-readable private directory")
	}
}
