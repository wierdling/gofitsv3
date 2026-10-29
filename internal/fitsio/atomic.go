package fitsio

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// atomicFile is the subset of *os.File used by WriteFileAtomic. Tests
// substitute implementations whose Sync or Close fail.
type atomicFile interface {
	io.Writer
	Chmod(os.FileMode) error
	Sync() error
	Close() error
	Name() string
}

var createAtomicTemp = func(dir, pattern string) (atomicFile, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// WriteFileAtomic writes path through a sibling temporary file. The data is
// flushed, synced and closed with every error checked before the temporary
// file is renamed over path, so a failed write never creates or replaces the
// destination. The temporary file is removed on failure.
func WriteFileAtomic(path string, write func(io.Writer) error) error {
	return writeFileAtomic(path, true, write)
}

// CreateFileAtomic is WriteFileAtomic for destinations that must not already
// exist. The completed file is published with a hard link, which fails
// instead of overwriting an existing path. Where hard links are unsupported
// (FAT/exFAT, many network shares) it falls back to an existence check and a
// rename, which can only race with another writer of the same path.
func CreateFileAtomic(path string, write func(io.Writer) error) error {
	return writeFileAtomic(path, false, write)
}

// linkFile is os.Link; tests substitute a failing implementation.
var linkFile = os.Link

func writeFileAtomic(path string, replace bool, write func(io.Writer) error) (err error) {
	path, mode, err := resolveAtomicTarget(path)
	if err != nil {
		return err
	}
	f, err := createAtomicTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if err != nil {
			if !closed {
				_ = f.Close()
			}
			_ = os.Remove(tmp)
		}
	}()
	// CreateTemp uses 0600; keep an existing file's mode, else os.Create's.
	if err = f.Chmod(mode); err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	if err = write(bw); err != nil {
		return err
	}
	if err = bw.Flush(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	closed = true
	if err = f.Close(); err != nil {
		return err
	}
	if replace {
		return os.Rename(tmp, path)
	}
	return publishExclusive(tmp, path)
}

// resolveAtomicTarget returns the file to replace and the mode to publish it
// with. A symlink is written through to its target rather than replaced, and
// an existing file keeps its permissions. Other non-regular paths are refused.
func resolveAtomicTarget(path string) (string, os.FileMode, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, 0o644, nil
	}
	if err != nil {
		return "", 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if path, err = filepath.EvalSymlinks(path); err != nil {
			return "", 0, err
		}
		if info, err = os.Stat(path); err != nil {
			return "", 0, err
		}
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", path)
	}
	return path, info.Mode().Perm(), nil
}

func publishExclusive(tmp, path string) error {
	err := linkFile(tmp, path)
	if err == nil {
		// The destination is published; a leftover temporary name is harmless.
		_ = os.Remove(tmp)
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return err
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		return &fs.PathError{Op: "create", Path: path, Err: fs.ErrExist}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	return os.Rename(tmp, path)
}
