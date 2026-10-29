package fitsio

import (
	"bufio"
	"io"
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
// instead of overwriting an existing path.
func CreateFileAtomic(path string, write func(io.Writer) error) error {
	return writeFileAtomic(path, false, write)
}

func writeFileAtomic(path string, replace bool, write func(io.Writer) error) (err error) {
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
	// CreateTemp uses 0600; published outputs keep the os.Create-style mode.
	if err = f.Chmod(0o644); err != nil {
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
	if err = os.Link(tmp, path); err != nil {
		return err
	}
	// The destination is published; a leftover temporary name is harmless.
	_ = os.Remove(tmp)
	return nil
}
