package asdfio

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// PixelToICRS is the format-independent native sky mapper used by Mosaic.
// Coordinates are zero-based detector pixels and ICRS longitude/latitude in
// degrees. Implementations are immutable and safe for concurrent calls.
type PixelToICRS interface {
	PixelToICRS(x, y float64) (ra, dec float64, err error)
}

// GWCSProfile identifies a registered native instrument model. Profiles are
// deliberately explicit: an unknown telescope/instrument must not silently
// acquire a placement approximation.
type GWCSProfile string

const (
	ProfileMIRIMIRIMAGE  GWCSProfile = "MIRI/MIRIMAGE"
	ProfileNIRCamModuleB GWCSProfile = "NIRCam/module-B"
)

// NativeGWCS is a compiled, immutable evaluator for a registered calibrated
// detector-to-ICRS model. The profile is retained for admission and auditing.
type NativeGWCS struct {
	root    *gwcsModel
	Profile GWCSProfile
}

// MIRIGWCS is retained as a source-compatible name for existing callers.
type MIRIGWCS = NativeGWCS

func (m *MIRIGWCS) PixelToICRS(x, y float64) (float64, float64, error) {
	if m == nil || m.root == nil {
		return 0, 0, fmt.Errorf("nil native GWCS")
	}
	v, err := m.root.eval([]float64{x, y})
	if err != nil {
		return 0, 0, err
	}
	if len(v) != 2 || !finite(v[0]) || !finite(v[1]) {
		return 0, 0, fmt.Errorf("native GWCS produced non-finite ICRS coordinates")
	}
	return v[0], v[1], nil
}

// CompileMIRIGWCS validates and compiles the four-step MIRI detector GWCS.
// Unknown model tags are rejected deliberately; silently approximating an
// unrecognised model would produce scientifically invalid mosaics.
func (d *Document) CompileMIRIGWCS() (*MIRIGWCS, error) {
	return d.CompileNativeGWCS(ProfileMIRIMIRIMAGE)
}

// CompileNativeGWCS validates and compiles the registered JWST imaging graph.
// MIRI and NIRCam calibrated ImageModel products currently share this native
// detector -> v2v3 -> v2v3vacorr -> world transform shape.
func (d *Document) CompileNativeGWCS(profile GWCSProfile) (*NativeGWCS, error) {
	if profile != ProfileMIRIMIRIMAGE && profile != ProfileNIRCamModuleB {
		return nil, fmt.Errorf("unsupported native GWCS profile %q", profile)
	}
	if err := validateRegisteredGWCSGraph(d.Root, profile); err != nil {
		return nil, err
	}
	meta := mappingValue(d.Root, "meta")
	wcs := mappingValue(meta, "wcs")
	steps := mappingValue(wcs, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode || len(steps.Content) != 4 {
		return nil, fmt.Errorf("unsupported native GWCS: expected four steps")
	}
	want := []string{"detector", "v2v3", "v2v3vacorr", "world"}
	models := make([]*gwcsModel, 0, 3)
	for i, step := range steps.Content {
		frame := mappingValue(mappingValue(step, "frame"), "name")
		if frame == nil || !strings.EqualFold(frame.Value, want[i]) {
			return nil, fmt.Errorf("unsupported native GWCS: step %d frame is not %s", i, want[i])
		}
		n := mappingValue(step, "transform")
		if i == 3 {
			if n != nil && !(n.Kind == yaml.ScalarNode && n.Value == "null") {
				return nil, fmt.Errorf("unsupported native GWCS: world step has a transform")
			}
			continue
		}
		model, err := compileGWCSNode(d, n)
		if err != nil {
			return nil, fmt.Errorf("native GWCS step %d: %w", i, err)
		}
		models = append(models, model)
	}
	return &NativeGWCS{root: &gwcsModel{kind: "pipeline", children: models}, Profile: profile}, nil
}

// validateRegisteredGWCSGraph is the admission boundary for native models.
// Keep this policy in asdfio so every caller of CompileNativeGWCS gets the
// same strict registration, including callers that do not use astroio.
func validateRegisteredGWCSGraph(root *yaml.Node, profile GWCSProfile) error {
	meta := mappingValue(root, "meta")
	instrument := mappingValue(meta, "instrument")
	name := mappingScalar(instrument, "name")
	detector := mappingScalar(instrument, "detector")
	module := mappingScalar(instrument, "module")
	switch profile {
	case ProfileMIRIMIRIMAGE:
		if !strings.EqualFold(name, "MIRI") || !strings.EqualFold(detector, "MIRIMAGE") {
			return fmt.Errorf("unsupported native GWCS %s profile: instrument metadata mismatch", profile)
		}
	case ProfileNIRCamModuleB:
		if !strings.EqualFold(name, "NIRCAM") || !strings.EqualFold(module, "B") {
			return fmt.Errorf("unsupported native GWCS %s profile: instrument metadata mismatch", profile)
		}
		switch strings.ToUpper(detector) {
		case "NRCB1", "NRCB2", "NRCB3", "NRCB4", "NRCBLONG":
		default:
			return fmt.Errorf("unsupported native GWCS %s profile: detector metadata mismatch", profile)
		}
	}
	wcs := mappingValue(meta, "wcs")
	steps := mappingValue(wcs, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode || len(steps.Content) != 4 {
		return fmt.Errorf("unsupported native GWCS: expected four registered steps")
	}
	want := []string{"detector", "v2v3", "v2v3vacorr", "world"}
	for i, step := range steps.Content {
		frame := mappingValue(mappingValue(step, "frame"), "name")
		if frame == nil || !strings.EqualFold(frame.Value, want[i]) {
			return fmt.Errorf("unsupported native GWCS: step %d frame is not %s", i, want[i])
		}
		transform := mappingValue(step, "transform")
		if i == 3 {
			if transform != nil && !(transform.Kind == yaml.ScalarNode && strings.EqualFold(transform.Value, "null")) {
				return fmt.Errorf("unsupported native GWCS: world step has a transform")
			}
			continue
		}
		if transform == nil || !strings.Contains(transform.Tag, "transform/compose-") {
			return fmt.Errorf("unsupported native GWCS: step %d is not a composed registered transform", i)
		}
	}
	// These operations distinguish the registered calibrated detector graph
	// from a simplified four-step graph that happens to use the same frames.
	var tags []string
	collectGWCSNodeTags(wcs, &tags)
	for _, marker := range []string{"transform/concatenate-", "transform/polynomial-", "transform/remap_axes-", "transform/scale-", "transform/shift-", "spherical_cartesian-", "rotate_sequence_3d-"} {
		found := false
		for _, tag := range tags {
			if strings.Contains(tag, marker) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unsupported native GWCS %s profile: missing %s operation", profile, marker)
		}
	}
	return nil
}

func collectGWCSNodeTags(n *yaml.Node, tags *[]string) {
	n = resolveAlias(n)
	if n == nil {
		return
	}
	if n.Tag != "" {
		*tags = append(*tags, n.Tag)
	}
	for _, child := range n.Content {
		collectGWCSNodeTags(child, tags)
	}
}

type gwcsModel struct {
	kind           string
	children       []*gwcsModel
	coeff          []float64
	domain, window []float64
	factor, offset float64
	mapping        []int
	angles         []float64
	axes           string
	transformType  string
}

func compileGWCSNode(d *Document, n *yaml.Node) (*gwcsModel, error) {
	n = resolveAlias(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("missing transform")
	}
	kind := strings.ToLower(n.Tag)
	model := &gwcsModel{}
	switch {
	case strings.Contains(kind, "transform/compose-"):
		model.kind = "compose"
		seq := mappingValue(n, "forward")
		if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
			return nil, fmt.Errorf("compose transform has no forward models")
		}
		for _, child := range seq.Content {
			c, err := compileGWCSNode(d, child)
			if err != nil {
				return nil, err
			}
			model.children = append(model.children, c)
		}
	case strings.Contains(kind, "transform/concatenate-"):
		model.kind = "concatenate"
		seq := mappingValue(n, "forward")
		if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
			return nil, fmt.Errorf("concatenate transform has no forward models")
		}
		for _, child := range seq.Content {
			c, err := compileGWCSNode(d, child)
			if err != nil {
				return nil, err
			}
			model.children = append(model.children, c)
		}
	case strings.Contains(kind, "transform/polynomial-"):
		model.kind = "polynomial"
		c := mappingValue(n, "coefficients")
		if c == nil {
			return nil, fmt.Errorf("polynomial transform has no coefficients")
		}
		var err error
		model.coeff, err = d.readCoefficient(c)
		if err != nil {
			return nil, err
		}
		model.domain = scalarList(mappingValue(n, "domain"))
		model.window = scalarList(mappingValue(n, "window"))
		if len(model.domain) == 0 || len(model.window) == 0 {
			return nil, fmt.Errorf("polynomial transform has no domain/window")
		}
	case strings.Contains(kind, "transform/identity-"):
		model.kind = "identity"
	case strings.Contains(kind, "transform/remap_axes-"):
		model.kind = "remap"
		v := mappingValue(n, "mapping")
		if v == nil || v.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("remap_axes transform has no mapping")
		}
		for _, x := range v.Content {
			var i int
			if _, err := fmt.Sscan(x.Value, &i); err != nil || i < 0 {
				return nil, fmt.Errorf("invalid remap_axes mapping")
			}
			model.mapping = append(model.mapping, i)
		}
	case strings.Contains(kind, "transform/scale-"):
		model.kind = "scale"
		if _, err := fmt.Sscan(mappingScalar(n, "factor"), &model.factor); err != nil || !finite(model.factor) {
			return nil, fmt.Errorf("invalid scale factor")
		}
	case strings.Contains(kind, "transform/shift-"):
		model.kind = "shift"
		if _, err := fmt.Sscan(mappingScalar(n, "offset"), &model.offset); err != nil || !finite(model.offset) {
			return nil, fmt.Errorf("invalid shift offset")
		}
	case strings.Contains(kind, "spherical_cartesian-"):
		model.kind = "spherical"
		model.transformType = strings.ToLower(mappingScalar(n, "transform_type"))
		if model.transformType != "spherical_to_cartesian" && model.transformType != "cartesian_to_spherical" {
			return nil, fmt.Errorf("unsupported spherical transform type %q", model.transformType)
		}
	case strings.Contains(kind, "rotate_sequence_3d-"):
		model.kind = "rotate"
		model.axes = mappingScalar(n, "axes_order")
		model.angles = scalarList(mappingValue(n, "angles"))
		if len(model.axes) != len(model.angles) {
			return nil, fmt.Errorf("rotation axes/angles mismatch")
		}
	default:
		return nil, fmt.Errorf("unsupported transform tag %q", n.Tag)
	}
	return model, nil
}

func (d *Document) readCoefficient(n *yaml.Node) ([]float64, error) {
	n = resolveAlias(n)
	if n.Kind != yaml.MappingNode || !strings.Contains(n.Tag, "ndarray") {
		return nil, fmt.Errorf("coefficient is not an ndarray")
	}
	var source int
	var count int64
	var dtype, order string
	var shape []int64
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		switch k {
		case "source":
			if _, e := fmt.Sscan(v.Value, &source); e != nil {
				return nil, e
			}
		case "datatype":
			dtype = v.Value
		case "byteorder":
			order = v.Value
		case "shape":
			for _, x := range v.Content {
				var z int64
				if _, e := fmt.Sscan(x.Value, &z); e != nil || z <= 0 {
					return nil, fmt.Errorf("invalid coefficient shape")
				}
				shape = append(shape, z)
			}
		}
	}
	if dtype != "float64" || (order != "little" && order != "big" && order != "=") || len(shape) == 0 || source < 0 || source >= len(d.Blocks) {
		return nil, fmt.Errorf("unsupported coefficient descriptor")
	}
	count = 1
	for _, z := range shape {
		if z > math.MaxInt64/count {
			return nil, fmt.Errorf("coefficient shape too large")
		}
		count *= z
	}
	b := d.Blocks[source]
	if b.Size != count*8 {
		return nil, fmt.Errorf("coefficient block %d has wrong size", source)
	}
	f, err := osOpen(d.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw := make([]byte, b.Size)
	if _, err = f.ReadAt(raw, b.PayloadOffset); err != nil {
		return nil, err
	}
	if err := verifyChecksum(raw, b.Checksum[:]); err != nil {
		return nil, err
	}
	out := make([]float64, int(count))
	var bo binary.ByteOrder = binary.LittleEndian
	if order == "big" {
		bo = binary.BigEndian
	}
	for i := range out {
		out[i] = math.Float64frombits(bo.Uint64(raw[i*8:]))
		if !finite(out[i]) {
			return nil, fmt.Errorf("coefficient contains non-finite value")
		}
	}
	return out, nil
}

// osOpen is a variable solely to keep coefficient I/O easy to replace in tests.
var osOpen = func(path string) (*os.File, error) { return os.Open(path) }

func (m *gwcsModel) eval(v []float64) ([]float64, error) {
	switch m.kind {
	case "pipeline":
		for _, c := range m.children {
			var err error
			v, err = c.eval(v)
			if err != nil {
				return nil, err
			}
		}
		return v, nil
	case "compose":
		for _, c := range m.children {
			var err error
			v, err = c.eval(v)
			if err != nil {
				return nil, err
			}
		}
		return v, nil
	case "concatenate":
		out := make([]float64, 0, len(m.children))
		for i, c := range m.children {
			args := v
			// GWCS concatenate models apply each child to its own contiguous
			// input tuple (the serialized input names are x0,y0,x1,y1...).
			// Preserve that tuple routing instead of letting every polynomial
			// accidentally consume the first two coordinates.
			if len(m.children) > 1 && len(v)%len(m.children) == 0 {
				width := len(v) / len(m.children)
				args = v[i*width : (i+1)*width]
			}
			x, err := c.eval(args)
			if err != nil {
				return nil, err
			}
			if len(x) == 0 {
				return nil, fmt.Errorf("transform returned no outputs")
			}
			out = append(out, x[0])
		}
		return out, nil
	case "identity":
		return append([]float64(nil), v...), nil
	case "remap":
		out := make([]float64, len(m.mapping))
		for i, j := range m.mapping {
			if j >= len(v) {
				return nil, fmt.Errorf("remap index out of range")
			}
			out[i] = v[j]
		}
		return out, nil
	case "scale":
		out := append([]float64(nil), v...)
		for i := range out {
			out[i] *= m.factor
		}
		return out, nil
	case "shift":
		out := append([]float64(nil), v...)
		for i := range out {
			out[i] += m.offset
		}
		return out, nil
	case "polynomial":
		return []float64{evalPoly(m.coeff, m.domain, m.window, v)}, nil
	case "spherical":
		if m.transformType == "spherical_to_cartesian" {
			lon, lat := v[0]*math.Pi/180, v[1]*math.Pi/180
			cl := math.Cos(lat)
			return []float64{cl * math.Cos(lon), cl * math.Sin(lon), math.Sin(lat)}, nil
		}
		r := math.Hypot(v[0], math.Hypot(v[1], v[2]))
		lon := math.Atan2(v[1], v[0]) * 180 / math.Pi
		if lon < 0 {
			lon += 360
		}
		return []float64{lon, math.Asin(v[2]/r) * 180 / math.Pi}, nil
	case "rotate":
		return rotate(v, m.angles, m.axes), nil
	}
	return nil, fmt.Errorf("unknown compiled transform %q", m.kind)
}

func evalPoly(c, d, w, v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	x := v[0]
	if len(d) >= 2 {
		x = (2*x - d[0] - d[1]) / (d[1] - d[0])
	}
	if len(c) == 2 {
		p := c[1]*x + c[0]
		if len(w) >= 2 {
			return (p+1)*(w[1]-w[0])/2 + w[0]
		}
		return p
	}
	y := 0.
	if len(v) > 1 {
		y = v[1]
	}
	if len(d) >= 4 {
		y = (2*y - d[2] - d[3]) / (d[3] - d[2])
	}
	p := 0.
	nx := int(math.Sqrt(float64(len(c))))
	for i := 0; i < nx; i++ {
		for j := 0; j < nx && i*nx+j < len(c); j++ {
			p += c[i*nx+j] * math.Pow(x, float64(i)) * math.Pow(y, float64(j))
		}
	}
	if len(w) >= 4 {
		return (p+1)*(w[1]-w[0])/2 + w[0]
	}
	return p
}

func rotate(v, a []float64, axes string) []float64 {
	out := append([]float64(nil), v...)
	for i, ax := range axes {
		if len(out) < 3 {
			break
		}
		// GWCS RotateSequence3D uses the passive-coordinate convention for
		// serialized angles. Our matrices are active rotations of the vector,
		// so the angle must be negated to represent the same transform.
		ang := -a[i] * math.Pi / 180
		c, s := math.Cos(ang), math.Sin(ang)
		x, y, z := out[0], out[1], out[2]
		switch ax {
		case 'x':
			out = []float64{x, c*y - s*z, s*y + c*z}
		case 'y':
			out = []float64{c*x + s*z, y, -s*x + c*z}
		case 'z':
			out = []float64{c*x - s*y, s*x + c*y, z}
		}
	}
	return out
}
func scalarList(n *yaml.Node) []float64 {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.SequenceNode {
		out := []float64{}
		for _, x := range n.Content {
			if x.Kind == yaml.SequenceNode {
				out = append(out, scalarList(x)...)
			} else {
				var v float64
				if _, e := fmt.Sscan(x.Value, &v); e != nil {
					return nil
				}
				out = append(out, v)
			}
		}
		return out
	}
	var v float64
	if _, e := fmt.Sscan(n.Value, &v); e != nil {
		return nil
	}
	return []float64{v}
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	n = resolveAlias(n)
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

// resolveAlias follows YAML aliases, including aliases that point forward to
// an anchor. A visited set prevents malformed cyclic aliases from recursing.
func resolveAlias(n *yaml.Node) *yaml.Node {
	seen := map[*yaml.Node]bool{}
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil && !seen[n] {
		seen[n] = true
		n = n.Alias
	}
	return n
}

func mappingScalar(n *yaml.Node, key string) string {
	v := mappingValue(n, key)
	if v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}
