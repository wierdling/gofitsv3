package fitsio

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// failingAtomicFile wraps a real temp file and injects Sync/Close failures.
type failingAtomicFile struct {
	*os.File
	syncErr, closeErr error
}

func (f *failingAtomicFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func (f *failingAtomicFile) Close() error {
	err := f.File.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func injectAtomicFailure(t *testing.T, syncErr, closeErr error) {
	t.Helper()
	orig := createAtomicTemp
	createAtomicTemp = func(dir, pattern string) (atomicFile, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		return &failingAtomicFile{File: f, syncErr: syncErr, closeErr: closeErr}, nil
	}
	t.Cleanup(func() { createAtomicTemp = orig })
}

func assertOnlyEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir entries = %v, want %v", names, want)
	}
	for i, e := range entries {
		if e.Name() != want[i] {
			t.Fatalf("dir entry %q, want %q", e.Name(), want[i])
		}
	}
}

func TestWriteFileAtomicFailuresLeaveNoDestination(t *testing.T) {
	injected := errors.New("injected")
	cases := []struct {
		name              string
		syncErr, closeErr error
		writeErr          error
	}{
		{name: "write", writeErr: injected},
		{name: "sync", syncErr: injected},
		{name: "close", closeErr: injected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			injectAtomicFailure(t, tc.syncErr, tc.closeErr)
			dir := t.TempDir()
			path := filepath.Join(dir, "out.fits")
			err := WriteFileAtomic(path, func(w io.Writer) error {
				if _, err := w.Write([]byte("data")); err != nil {
					return err
				}
				return tc.writeErr
			})
			if !errors.Is(err, injected) {
				t.Fatalf("err = %v, want injected failure", err)
			}
			assertOnlyEntries(t, dir)
		})
	}
}

func TestWriteFileAtomicFailurePreservesExistingDestination(t *testing.T) {
	injectAtomicFailure(t, nil, errors.New("injected close"))
	dir := t.TempDir()
	path := filepath.Join(dir, "out.fits")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, func(w io.Writer) error {
		_, err := w.Write([]byte("new"))
		return err
	}); err == nil {
		t.Fatal("expected close failure")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "old" {
		t.Fatalf("destination = %q, %v; want old", got, err)
	}
	assertOnlyEntries(t, dir, "out.fits")
}

func TestWriteFileAtomicReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.fits")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, func(w io.Writer) error {
		_, err := w.Write([]byte("new"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Fatalf("destination = %q, %v; want new", got, err)
	}
	assertOnlyEntries(t, dir, "out.fits")
}

func TestCreateFileAtomicDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mask.fits")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CreateFileAtomic(path, func(w io.Writer) error {
		_, err := w.Write([]byte("new"))
		return err
	})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("err = %v, want ErrExist", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("destination overwritten: %q", got)
	}
	assertOnlyEntries(t, dir, "mask.fits")

	fresh := filepath.Join(dir, "new.fits")
	if err := CreateFileAtomic(fresh, func(w io.Writer) error {
		_, err := w.Write([]byte("new"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertOnlyEntries(t, dir, "mask.fits", "new.fits")
}

func TestWriteFloat32ImageCloseFailureLeavesNoFile(t *testing.T) {
	injectAtomicFailure(t, nil, errors.New("injected close"))
	dir := t.TempDir()
	path := filepath.Join(dir, "out.fits")
	err := WriteFloat32Image(path, Header{}, ImageData{Width: 2, Height: 2, Pixels: []float32{1, 2, 3, 4}})
	if err == nil {
		t.Fatal("expected close failure")
	}
	assertOnlyEntries(t, dir)
}
