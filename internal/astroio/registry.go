package astroio

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func probeMagicOrExtension(path string, magic []byte, extensions ...string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	b := make([]byte, len(magic))
	n, readErr := f.Read(b)
	if n == len(magic) && string(b) == string(magic) {
		return true, nil
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, readErr
	}
	if readErr != nil && n == 0 {
		// An empty file can still be identified by an accepted extension; the
		// decoder will return the useful parse error on Open.
		readErr = nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, accepted := range extensions {
		if ext == accepted {
			return true, nil
		}
	}
	return false, nil
}
