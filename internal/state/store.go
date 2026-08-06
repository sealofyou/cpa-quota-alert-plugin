package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/monitor"
)

type File interface {
	io.Reader
	io.Writer
	Close() error
	Sync() error
	Chmod(mode os.FileMode) error
}

type FS interface {
	Open(name string) (File, error)
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	Chmod(name string, mode os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
}

type OSFS struct{}

func (OSFS) Open(name string) (File, error) { return os.Open(name) }
func (OSFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	return os.OpenFile(name, flag, perm)
}
func (OSFS) Rename(oldpath, newpath string) error { return replaceFile(oldpath, newpath) }
func (OSFS) Remove(name string) error             { return os.Remove(name) }
func (OSFS) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}
func (OSFS) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

type Store struct {
	Path string
	FS   FS
}

func Load(path string) (monitor.State, error) {
	return Store{Path: path, FS: OSFS{}}.Load()
}

func Save(path string, state monitor.State) error {
	return Store{Path: path, FS: OSFS{}}.Save(state)
}

func (s Store) Load() (monitor.State, error) {
	fs := s.fs()
	file, err := fs.Open(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return monitor.State{}, nil
		}
		return monitor.State{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return monitor.State{}, err
	}
	var probe any
	if err := json.Unmarshal(data, &probe); err != nil {
		return monitor.State{}, err
	}
	if _, ok := probe.(map[string]any); !ok {
		return monitor.State{}, errors.New("state file must contain a JSON object")
	}
	var state monitor.State
	if err := json.Unmarshal(data, &state); err != nil {
		return monitor.State{}, err
	}
	return state, nil
}

func (s Store) Save(state monitor.State) error {
	fs := s.fs()
	dir := filepath.Dir(s.Path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmpName := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d-%d", filepath.Base(s.Path), os.Getpid(), time.Now().UnixNano()))
	tmp, err := fs.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	closed := false
	cleanup := func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = fs.Remove(tmpName)
	}
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if err := writeAll(tmp, data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		closed = true
		cleanup()
		return err
	}
	closed = true
	if err := fs.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return err
	}
	if err := fs.Rename(tmpName, s.Path); err != nil {
		cleanup()
		return fmt.Errorf("atomic state rename: %w", err)
	}
	_ = fs.Chmod(s.Path, 0o600)
	syncDir(fs, dir)
	return nil
}

func writeAll(file File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (s Store) fs() FS {
	if s.FS != nil {
		return s.FS
	}
	return OSFS{}
}

func syncDir(fs FS, dir string) {
	file, err := fs.Open(dir)
	if err != nil {
		return
	}
	defer file.Close()
	_ = file.Sync()
}
