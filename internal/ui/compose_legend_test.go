package ui

import (
	"errors"
	"image"
	"io"
	"testing"
)

type legendCloseWriter struct {
	io.Writer
	err    error
	closed bool
}

func (w *legendCloseWriter) Close() error { w.closed = true; return w.err }

func TestEncodeLegendPNGReportsCloseError(t *testing.T) {
	w := &legendCloseWriter{Writer: io.Discard, err: errors.New("close failed")}
	if err := encodeLegendPNG(w, w, image.NewRGBA(image.Rect(0, 0, 1, 1))); err == nil || err.Error() != "close failed" {
		t.Fatalf("error = %v, want close failed", err)
	}
	if !w.closed {
		t.Fatal("writer was not closed")
	}
}

type failingLegendWriter struct{}

func (failingLegendWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestEncodeLegendPNGReportsEncodeErrorAndStillCloses(t *testing.T) {
	w := &legendCloseWriter{Writer: failingLegendWriter{}, err: errors.New("close failed")}
	err := encodeLegendPNG(w, w, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	if err == nil || err.Error() != "write failed" {
		t.Fatalf("error = %v, want write failed", err)
	}
	if !w.closed {
		t.Fatal("writer was not closed")
	}
}
