package mosaic

import (
	"testing"

	"gofitsv3/internal/processing"
)

func fallbackTestStar(x, y, flux float64) processing.Star {
	return processing.Star{X: x, Y: y, Flux: flux}
}

func TestShouldRetryRawCatalogRetriesWhenSourceWasFiltered(t *testing.T) {
	raw := []processing.Star{fallbackTestStar(10, 20, 100), fallbackTestStar(30, 40, 50)}
	consensus := raw[:1]
	if !shouldRetryRawCatalog(consensus, raw, raw, raw) {
		t.Fatal("expected raw retry when consensus removed a source star")
	}
}

func TestShouldRetryRawCatalogRetriesWhenReferenceWasFiltered(t *testing.T) {
	raw := []processing.Star{fallbackTestStar(10, 20, 100), fallbackTestStar(30, 40, 50)}
	consensus := raw[:1]
	if !shouldRetryRawCatalog(raw, raw, consensus, raw) {
		t.Fatal("expected raw retry when consensus removed a reference star")
	}
}

func TestShouldRetryRawCatalogSkipsIdenticalCatalogs(t *testing.T) {
	consensus := []processing.Star{fallbackTestStar(10, 20, 100), fallbackTestStar(30, 40, 50)}
	raw := append([]processing.Star(nil), consensus...)
	if shouldRetryRawCatalog(consensus, raw, consensus, raw) {
		t.Fatal("did not expect a duplicate raw retry for identical catalogs")
	}
}
