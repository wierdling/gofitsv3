package astroio

import (
	"context"
	"fmt"
)

// Open probes registered decoders in registration order and opens the first
// matching source.
func Open(ctx context.Context, path string) (Source, error) {
	return DefaultRegistry.Open(ctx, path)
}

// Registry holds format decoders. It is intentionally small so adding ASDF
// does not require changing callers.
type Registry struct {
	decoders []Decoder
}

func (r *Registry) Probe(path string) (Decoder, bool, error) {
	for _, decoder := range r.decoders {
		matched, err := decoder.Probe(path)
		if err != nil {
			return nil, false, err
		}
		if matched {
			return decoder, true, nil
		}
	}
	return nil, false, nil
}

func (r *Registry) Register(decoder Decoder) {
	if decoder != nil {
		r.decoders = append(r.decoders, decoder)
	}
}

func (r *Registry) Open(ctx context.Context, path string) (Source, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, decoder := range r.decoders {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		matched, err := decoder.Probe(path)
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", decoder.Name(), err)
		}
		if matched {
			return decoder.Open(ctx, path)
		}
	}
	return nil, fmt.Errorf("unsupported astronomy file format: %s", path)
}

var DefaultRegistry = func() Registry {
	var r Registry
	r.Register(FITSDecoder{})
	r.Register(ASDFDecoder{})
	return r
}()
