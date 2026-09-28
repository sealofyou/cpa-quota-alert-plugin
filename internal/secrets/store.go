package secrets

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/state"
)

const maxFileBytes = 32 * 1024

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Store keeps notification values outside CPA YAML and plugin state.
type Store struct {
	Path string
	mu   sync.Mutex
}

func Default() (*Store, error) {
	if runtime.GOOS != "windows" {
		if first := strings.Split(os.Getenv("STATE_DIRECTORY"), ":")[0]; filepath.IsAbs(first) {
			return &Store{Path: filepath.Join(first, "notification-secrets.json")}, nil
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return nil, errors.New("private config directory unavailable")
	}
	return &Store{Path: filepath.Join(dir, "cpa-quota-alert-plugin", "notification-secrets.json")}, nil
}

func (s *Store) Names() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) Lookup(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return "", false
	}
	value, ok := values[name]
	return value, ok
}

func (s *Store) Update(set map[string]string, clear []string) error {
	for name, value := range set {
		if !envName.MatchString(name) || strings.TrimSpace(value) == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid notification value")
		}
	}
	for _, name := range clear {
		if !envName.MatchString(name) {
			return errors.New("invalid notification name")
		}
		if _, duplicate := set[name]; duplicate {
			return errors.New("notification name cannot be set and cleared together")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return err
	}
	for _, name := range clear {
		delete(values, name)
	}
	for name, value := range set {
		values[name] = value
	}
	return s.save(values)
}

func (s *Store) load() (map[string]string, error) {
	if s == nil || s.Path == "" {
		return nil, errors.New("private store unavailable")
	}
	if err := checkPrivateDir(filepath.Dir(s.Path), false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return nil, errors.New("private store permissions invalid")
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return nil, errors.New("private store unreadable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return nil, errors.New("private store unreadable")
	}
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil || values == nil {
		return nil, errors.New("private store invalid")
	}
	return values, nil
}

func (s *Store) save(values map[string]string) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("private store directory unavailable")
	}
	if err := checkPrivateDir(dir, true); err != nil {
		return err
	}
	data, err := json.Marshal(values)
	if err != nil || len(data) > maxFileBytes {
		return errors.New("private store too large")
	}
	tmp, err := os.CreateTemp(dir, ".notification-secrets-*")
	if err != nil {
		return errors.New("private store write failed")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0o600); err != nil {
		return errors.New("private store permissions invalid")
	}
	if _, err := tmp.Write(data); err != nil {
		return errors.New("private store write failed")
	}
	if err := tmp.Sync(); err != nil {
		return errors.New("private store write failed")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("private store write failed")
	}
	if err := state.ReplaceFile(tmp.Name(), s.Path); err != nil {
		return errors.New("private store replace failed")
	}
	return nil
}

func checkPrivateDir(dir string, mustExist bool) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !mustExist {
			return err
		}
		return errors.New("private store directory unavailable")
	}
	if !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return errors.New("private store directory permissions invalid")
	}
	return nil
}
