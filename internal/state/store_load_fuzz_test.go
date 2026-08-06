package state

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

func FuzzStoreLoad(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"low_active":true,"pending_events":[{"id":"evt-test","kind":"low","created_at":"2026-08-06T00:00:00Z","delivery":{"smtp":"pending"}}]}`),
		[]byte(`{"plan_changed_active":true,"pending_events":[{"id":"evt-plan","kind":"plan_changed","created_at":"2026-08-06T00:00:00Z","delivery":{"webhook":"pending"}}]}`),
		[]byte(`[]`),
		[]byte(`{"low_active":`),
		[]byte(``),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = (Store{Path: "state.json", FS: readOnlyFS{data: append([]byte(nil), data...)}}).Load()
	})
}

type readOnlyFS struct {
	data []byte
}

func (fs readOnlyFS) Open(string) (File, error) {
	return &readOnlyFile{Reader: bytes.NewReader(fs.data)}, nil
}

func (readOnlyFS) OpenFile(string, int, os.FileMode) (File, error) {
	return nil, errors.New("read-only fs")
}

func (readOnlyFS) Rename(string, string) error { return errors.New("read-only fs") }
func (readOnlyFS) Remove(string) error         { return errors.New("read-only fs") }
func (readOnlyFS) Chmod(string, os.FileMode) error {
	return errors.New("read-only fs")
}
func (readOnlyFS) MkdirAll(string, os.FileMode) error { return errors.New("read-only fs") }

type readOnlyFile struct {
	io.Reader
}

func (readOnlyFile) Write([]byte) (int, error) { return 0, errors.New("read-only file") }
func (readOnlyFile) Close() error              { return nil }
func (readOnlyFile) Sync() error               { return nil }
func (readOnlyFile) Chmod(os.FileMode) error   { return errors.New("read-only file") }
