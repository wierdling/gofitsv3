package badpix

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanWithDQ(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mini.fits")

	sci := []float32{
		1, 2, 3,
		4, 50, 6,
		7, 8, 9,
	}
	dq := []uint16{
		0, 0, 0,
		0, 1, 0,
		0, 0, 0,
	}
	if err := writeMiniFITS(path, sci, dq, 3, 3); err != nil {
		t.Fatalf("write fits: %v", err)
	}

	cleaned, mask, err := Clean(path, Config{})
	if err != nil {
		t.Fatalf("clean failed: %v", err)
	}
	if len(mask) != len(sci) {
		t.Fatalf("mask length mismatch")
	}
	center := 4
	if !mask[center] {
		t.Fatalf("expected center pixel masked")
	}
	if cleaned.Pixels[center] == sci[center] {
		t.Fatalf("center pixel was not changed")
	}
	// good pixel unchanged
	if cleaned.Pixels[0] != sci[0] {
		t.Fatalf("good pixel changed")
	}
}

func writeMiniFITS(path string, sci []float32, dq []uint16, w, h int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	writeHeader := func(cards []string) error {
		var block []byte
		for _, c := range cards {
			if len(c) > 80 {
				c = c[:80]
			}
			if len(c) < 80 {
				c = c + spaces(80-len(c))
			}
			block = append(block, []byte(c)...)
		}
		block = append(block, []byte(spaces(padding(len(block))))...)
		_, err := f.Write(block)
		return err
	}

	primary := []string{
		card("SIMPLE", "T"),
		card("BITPIX", "-32"),
		card("NAXIS", "2"),
		card("NAXIS1", fmt.Sprintf("%d", w)),
		card("NAXIS2", fmt.Sprintf("%d", h)),
		card("EXTEND", "T"),
		card("END", ""),
	}
	if err := writeHeader(primary); err != nil {
		return err
	}

	// write SCI data float32 big endian
	if err := binary.Write(f, binary.BigEndian, sci); err != nil {
		return err
	}
	if _, err := f.Write(make([]byte, padding(len(sci)*4))); err != nil {
		return err
	}

	// DQ extension header
	dqHeader := []string{
		card("XTENSION", "'IMAGE   '"),
		card("BITPIX", "16"),
		card("NAXIS", "2"),
		card("NAXIS1", fmt.Sprintf("%d", w)),
		card("NAXIS2", fmt.Sprintf("%d", h)),
		card("EXTNAME", "'DQ'"),
		card("END", ""),
	}
	if err := writeHeader(dqHeader); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, dq); err != nil {
		return err
	}
	if _, err := f.Write(make([]byte, padding(len(dq)*2))); err != nil {
		return err
	}
	return nil
}

func card(key, val string) string {
	if val == "" {
		return fmt.Sprintf("%-8s", key)
	}
	return fmt.Sprintf("%-8s= %-20s", key, val)
}

func padding(n int) int {
	mod := n % 2880
	if mod == 0 {
		return 0
	}
	return 2880 - mod
}

func spaces(n int) string {
	return fmt.Sprintf("%*s", n, "")
}
