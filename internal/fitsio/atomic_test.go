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

func TestCreateFileAtomicFallsBackWithoutHardLinks(t *testing.T) {
	orig := linkFile
	linkFile = func(string, string) error { return errors.New("operation not supported") }
	t.Cleanup(func() { linkFile = orig })

	dir := t.TempDir()
	path := filepath.Join(dir, "mask.fits")
	write := func(w io.Writer) error {
		_, err := w.Write([]byte("new"))
		return err
	}
	if err := CreateFileAtomic(path, write); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("destination = %q, want new", got)
	}
	if err := CreateFileAtomic(path, write); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second create err = %v, want ErrExist", err)
	}
	assertOnlyEntries(t, dir, "mask.fits")
}

func TestWriteFileAtomicKeepsModeAndWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.fits")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.fits")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WriteFileAtomic(link, func(w io.Writer) error {
		_, err := w.Write([]byte("new"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced by a regular file (%v)", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Fatalf("target = %q, want new", got)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("mode = %v, want %v", after.Mode().Perm(), before.Mode().Perm())
	}
}
