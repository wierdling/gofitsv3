package astroio

import (
	"context"
	"fmt"
	"strings"

	"gofitsv3/internal/fitsio"
)

// FITSDecoder adapts the existing FITS reader to Source.
type FITSDecoder struct{}

func (FITSDecoder) Name() string { return "FITS" }

func (FITSDecoder) Probe(path string) (bool, error) {
	return probeMagicOrExtension(path, []byte("SIMPLE"), ".fits", ".fit", ".fts")
}

func (FITSDecoder) Open(ctx context.Context, path string) (Source, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := fitsio.LoadFileMetadata(path); err != nil {
		return nil, err
	}
	return &fitsSource{path: path}, nil
}

type fitsSource struct{ path string }

func (s *fitsSource) Metadata(ctx context.Context) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	f, err := fitsio.LoadFileMetadata(s.path)
	if err != nil {
		return Metadata{}, err
	}
	meta := Metadata{Format: "FITS", Path: s.path, Planes: make([]PlaneInfo, 0)}
	for index, hdu := range f.HDUs {
		if err := ctx.Err(); err != nil {
			return Metadata{}, err
		}
		if index == 0 {
			meta.Cards = cloneCards(hdu.Header.Cards)
		}
		if hdu.Data.Width <= 0 || hdu.Data.Height <= 0 {
			continue
		}
		id := fitsPlaneID(index, hdu)
		meta.Planes = append(meta.Planes, PlaneInfo{ID: id, Name: hdu.ExtName, Kind: fitsPlaneKind(hdu.ExtName), Width: hdu.Data.Width, Height: hdu.Data.Height})
	}
	return meta, nil
}

func (s *fitsSource) ReadPlane(ctx context.Context, id PlaneID, options ReadOptions) (Plane, error) {
	if err := ctx.Err(); err != nil {
		return Plane{}, err
	}
	index, err := parseFITSPlaneID(id)
	if err != nil {
		return Plane{}, err
	}
	_, selected, err := fitsio.LoadHDUByIndex(s.path, index)
	if err != nil {
		return Plane{}, err
	}
	if selected.Data.Width <= 0 || selected.Data.Height <= 0 {
		return Plane{}, fmt.Errorf("plane %q is not a supported image", id)
	}
	kind := fitsPlaneKind(selected.ExtName)
	if options.ExactIntegerDQ && kind != "dq" {
		return Plane{}, fmt.Errorf("exact integer DQ requested for non-DQ plane %q", id)
	}
	out := Plane{ID: id, Name: selected.ExtName, Kind: kind, Width: selected.Data.Width, Height: selected.Data.Height, Data: selected.Data.Pixels}
	if options.ExactIntegerDQ {
		if len(selected.Data.Int32Pixels) != selected.Data.Width*selected.Data.Height {
			return Plane{}, fmt.Errorf("exact integer DQ samples unavailable for %q", id)
		}
		out.ExactDQ = make([]uint32, len(selected.Data.Int32Pixels))
		for i, value := range selected.Data.Int32Pixels {
			out.ExactDQ[i] = uint32(value)
		}
	}
	return out, nil
}

func (s *fitsSource) StreamPlane(ctx context.Context, id PlaneID, options ReadOptions, writer RowWriter) error {
	if writer == nil {
		return fmt.Errorf("row writer is required")
	}
	index, err := parseFITSPlaneID(id)
	if err != nil {
		return err
	}
	var kind string
	_, _, returnError := fitsio.StreamHDUByIndex(ctx, s.path, index, func(y int, values []float32, exact []int32) error {
		if kind == "" {
			// The callback's first row is reached only after the exact HDU has
			// been located; derive kind from metadata without decoding pixels.
			meta, err := s.Metadata(ctx)
			if err != nil {
				return err
			}
			for _, info := range meta.Planes {
				if info.ID == id {
					kind = info.Kind
					break
				}
			}
			if options.ExactIntegerDQ && kind != "dq" {
				return fmt.Errorf("exact integer DQ requested for non-DQ plane %q", id)
			}
		}
		row := Row{Y: y, Float32: values}
		if options.ExactIntegerDQ {
			if len(exact) != len(values) {
				return fmt.Errorf("exact integer DQ samples unavailable for %q", id)
			}
			row.ExactDQ = make([]uint32, len(exact))
			for i, value := range exact {
				row.ExactDQ[i] = uint32(value)
			}
		}
		return writer.WriteRow(ctx, row)
	})
	return returnError
}

func (p Plane) ExactDQSlice(start, end int) []uint32 {
	if p.ExactDQ == nil {
		return nil
	}
	return p.ExactDQ[start:end]
}

func fitsPlaneID(index int, hdu fitsio.HDU) PlaneID {
	return PlaneID(fmt.Sprintf("fits:hdu:%d", index))
}
func parseFITSPlaneID(id PlaneID) (int, error) {
	const prefix = "fits:hdu:"
	if !strings.HasPrefix(string(id), prefix) {
		return 0, fmt.Errorf("invalid FITS plane ID %q", id)
	}
	var index int
	if _, err := fmt.Sscanf(string(id)[len(prefix):], "%d", &index); err != nil || index < 0 {
		return 0, fmt.Errorf("invalid FITS plane ID %q", id)
	}
	return index, nil
}
func fitsPlaneKind(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dq":
		return "dq"
	case "err", "var_flat", "var_poisson", "var_rnoise":
		return "uncertainty"
	default:
		return "science"
	}
}
func cloneCards(cards map[string]string) map[string]string {
	out := make(map[string]string, len(cards))
	for k, v := range cards {
		out[k] = v
	}
	return out
}
