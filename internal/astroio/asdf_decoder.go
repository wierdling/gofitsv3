package astroio

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gofitsv3/internal/asdfio"
	"gopkg.in/yaml.v3"
)

// ASDFDecoder adapts the supported JWST ImageModel ASDF profile.
type ASDFDecoder struct{}

func (ASDFDecoder) Name() string { return "ASDF" }

func (ASDFDecoder) Probe(path string) (bool, error) {
	return probeMagicOrExtension(path, []byte("#ASDF"), ".asdf")
}

func (ASDFDecoder) Open(ctx context.Context, path string) (Source, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, err := asdfio.Open(path)
	if err != nil {
		return nil, err
	}
	var native PixelToICRS
	var profile string
	if p, ok := jwstGWCSProfile(d.Root); ok {
		if compiled, compileErr := d.CompileNativeGWCS(p); compileErr == nil {
			native, profile = compiled, string(p)
		}
	}
	return &asdfSource{document: d, nativeGWCS: native, nativeProfile: profile}, nil
}

type asdfSource struct {
	document      *asdfio.Document
	nativeGWCS    PixelToICRS
	nativeProfile string
}

func (s *asdfSource) Metadata(ctx context.Context) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	m := Metadata{Format: "ASDF", Path: s.document.Path, Cards: map[string]string{}, NativeGWCS: s.nativeGWCS, NativeGWCSProfile: s.nativeProfile}
	roles := make([]string, 0, len(s.document.Arrays))
	for role := range s.document.Arrays {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		a := s.document.Arrays[role]
		kind := "science"
		if role == "dq" {
			kind = "dq"
		} else if role != "data" {
			kind = "uncertainty"
		}
		m.Planes = append(m.Planes, PlaneInfo{ID: asdfPlaneID(role), Name: role, Kind: kind, Width: int(a.Shape[1]), Height: int(a.Shape[0])})
	}
	// Expose only the supported JWST metadata surface. In particular, do not
	// flatten arbitrary nested GWCS/schema values into pseudo-FITS cards.
	collectJWSTCards(s.document.Root, m.Cards)
	if s.nativeGWCS != nil {
		m.Cards["GWCSMODEL"] = nativeGWCSModelMarker(s.nativeProfile)
		m.Cards["GWCSPROFILE"] = s.nativeProfile
	}
	return m, nil
}

func collectJWSTCards(root *yaml.Node, cards map[string]string) {
	meta := mappingValue(root, "meta")
	wcs := mappingValue(meta, "wcsinfo")
	instrument := mappingValue(meta, "instrument")
	for _, key := range []string{"crpix1", "crpix2", "crval1", "crval2", "cdelt1", "cdelt2", "pc1_1", "pc1_2", "pc2_1", "pc2_2", "cd1_1", "cd1_2", "cd2_1", "cd2_2", "ctype1", "ctype2"} {
		if value := mappingScalar(wcs, key); value != "" {
			cards[strings.ToUpper(key)] = value
		}
	}
	// Native admission and its marker are set by Metadata only after the
	// registered compiler has accepted the complete graph. ASDF permits
	// arbitrary GWCS models, which must remain Examine-only.
	// These are ordinary JWST ImageModel metadata used for labels and units,
	// not a general recursive metadata export.
	for _, key := range []string{"filter", "pupil", "instrume", "detector", "exptime", "date-obs", "bunit"} {
		value := mappingScalar(meta, key)
		if value == "" {
			value = mappingScalar(instrument, key)
		}
		if value == "" && key == "instrume" {
			value = mappingScalar(instrument, "name")
		}
		if value == "" {
			value = mappingScalar(mappingValue(meta, "exposure"), key)
		}
		if value == "" {
			value = mappingScalar(mappingValue(meta, "observation"), key)
		}
		if value != "" {
			cards[strings.ToUpper(key)] = value
		}
	}
}

func nativeGWCSModelMarker(profile string) string {
	if profile == string(asdfio.ProfileMIRIMIRIMAGE) {
		return "MIRI_NATIVE_GWCS"
	}
	if profile == string(asdfio.ProfileNIRCamModuleB) {
		return "NIRCAM_NATIVE_GWCS"
	}
	return ""
}

func jwstGWCSProfile(root *yaml.Node) (asdfio.GWCSProfile, bool) {
	meta := mappingValue(root, "meta")
	if meta == nil {
		meta = root
	}
	instrumentNode := mappingValue(meta, "instrument")
	instrument := mappingScalar(instrumentNode, "name")
	detector := mappingScalar(instrumentNode, "detector")
	module := mappingScalar(instrumentNode, "module")
	if strings.EqualFold(instrument, "MIRI") && strings.EqualFold(detector, "MIRIMAGE") {
		return asdfio.ProfileMIRIMIRIMAGE, true
	}
	if strings.EqualFold(instrument, "NIRCAM") && strings.EqualFold(module, "B") {
		switch strings.ToUpper(detector) {
		case "NRCB1", "NRCB2", "NRCB3", "NRCB4", "NRCBLONG":
			return asdfio.ProfileNIRCamModuleB, true
		}
	}
	return "", false
}

func collectMappingScalars(n *yaml.Node, cards map[string]string) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if value.Kind == yaml.ScalarNode && value.Value != "" {
			cards[strings.ToUpper(key.Value)] = value.Value
		}
	}
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if strings.EqualFold(n.Content[i].Value, key) {
			return n.Content[i+1]
		}
	}
	return nil
}

func mappingScalar(n *yaml.Node, key string) string {
	v := mappingValue(n, key)
	if v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func collectScalarCards(n *yaml.Node, cards map[string]string) {
	if n == nil {
		return
	}
	if n.Kind == yaml.DocumentNode || n.Kind == yaml.SequenceNode {
		for _, child := range n.Content {
			collectScalarCards(child, cards)
		}
		return
	}
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if value.Kind == yaml.ScalarNode && value.Value != "" {
			cards[strings.ToUpper(key.Value)] = value.Value
			continue
		}
		collectScalarCards(value, cards)
	}
}

func (s *asdfSource) ReadPlane(ctx context.Context, id PlaneID, options ReadOptions) (Plane, error) {
	role, err := parseASDFPlaneID(id)
	if err != nil {
		return Plane{}, err
	}
	if options.ExactIntegerDQ && role != "dq" {
		return Plane{}, fmt.Errorf("exact integer DQ requested for non-DQ plane %q", id)
	}
	a := s.document.Arrays[role]
	p := Plane{ID: id, Name: role, Kind: asdfKind(role), Width: int(a.Shape[1]), Height: int(a.Shape[0]), Data: make([]float32, int(a.Shape[0]*a.Shape[1]))}
	if role == "dq" && options.ExactIntegerDQ {
		p.ExactDQ = make([]uint32, len(p.Data))
	}
	err = s.document.StreamArray(ctx, role, func(y int, values []float32, dqs []uint32) error {
		start := y * p.Width
		if role == "dq" {
			for i, v := range dqs {
				p.Data[start+i] = float32(v)
				if options.ExactIntegerDQ {
					p.ExactDQ[start+i] = v
				}
			}
		} else {
			copy(p.Data[start:], values)
		}
		return nil
	})
	if err != nil {
		return Plane{}, err
	}
	return p, nil
}

func (s *asdfSource) StreamPlane(ctx context.Context, id PlaneID, options ReadOptions, writer RowWriter) error {
	if writer == nil {
		return fmt.Errorf("row writer is required")
	}
	role, err := parseASDFPlaneID(id)
	if err != nil {
		return err
	}
	if options.ExactIntegerDQ && role != "dq" {
		return fmt.Errorf("exact integer DQ requested for non-DQ plane %q", id)
	}
	return s.document.StreamArray(ctx, role, func(y int, values []float32, dqs []uint32) error {
		row := Row{Y: y, Float32: values}
		if role == "dq" {
			if options.ExactIntegerDQ {
				row.ExactDQ = dqs
			} else {
				row.Float32 = make([]float32, len(dqs))
				for i, v := range dqs {
					row.Float32[i] = float32(v)
				}
			}
		}
		return writer.WriteRow(ctx, row)
	})
}

func asdfKind(role string) string {
	if role == "dq" {
		return "dq"
	}
	if role == "data" {
		return "science"
	}
	return "uncertainty"
}
func asdfPlaneID(role string) PlaneID { return PlaneID("asdf:" + role) }
func parseASDFPlaneID(id PlaneID) (string, error) {
	const prefix = "asdf:"
	role := strings.TrimPrefix(string(id), prefix)
	if role == string(id) || filepath.Ext(role) != "" {
		return "", fmt.Errorf("invalid ASDF plane ID %q", id)
	}
	if _, ok := map[string]bool{"data": true, "dq": true, "err": true, "var_flat": true, "var_poisson": true, "var_rnoise": true}[role]; !ok {
		return "", fmt.Errorf("invalid ASDF plane ID %q", id)
	}
	return role, nil
}
