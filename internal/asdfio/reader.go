// Package asdfio validates the small, JWST ImageModel subset of ASDF used by
// the first native reader. It deliberately does not decode array payloads.
package asdfio

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	Magic                     = "#ASDF"
	SupportedVersion          = "1.0.0"
	SupportedStandard         = "1.6.0"
	blockMagic         uint32 = 0xD3424C4B // D3BLK
	blockRemainderSize        = 48
	blockPreambleSize         = 6
	maxHeader                 = 64 << 20
)

type Block struct {
	Index                                  int
	Offset, PayloadOffset, Size, Allocated int64
	Flags                                  uint32
	Compression                            [4]byte
	Checksum                               [16]byte
}
type Array struct {
	Role             string
	Tag              string
	Source           int
	DType, ByteOrder string
	Shape            []int64
	Block            Block
}
type Document struct {
	Path              string
	Version, Standard string
	Root              *yaml.Node
	Arrays            map[string]Array
	Blocks            []Block
}

// Open validates an ASDF container and the observed JWST ImageModel profile.
// No block payload is read or allocated.
func Open(path string) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f, path)
}

func Parse(r io.ReaderAt, path string) (*Document, error) {
	h, err := readHeader(r)
	if err != nil {
		return nil, err
	}
	root := new(yaml.Node)
	if err := yaml.Unmarshal(h.yaml, root); err != nil {
		return nil, fmt.Errorf("asdf YAML: %w", err)
	}
	d := &Document{Path: path, Version: h.version, Standard: h.standard, Root: root, Arrays: map[string]Array{}}
	if d.Version != SupportedVersion {
		return nil, fmt.Errorf("unsupported ASDF version %q (supported %s)", d.Version, SupportedVersion)
	}
	if d.Standard != SupportedStandard {
		return nil, fmt.Errorf("unsupported ASDF standard %q (supported %s)", d.Standard, SupportedStandard)
	}
	blocks, err := scanBlocks(r, h.start)
	if err != nil {
		return nil, err
	}
	d.Blocks = blocks
	if err := validateTree(root, d); err != nil {
		return nil, err
	}
	return d, nil
}

type header struct {
	yaml              []byte
	start             int64
	version, standard string
}

func readHeader(r io.ReaderAt) (header, error) {
	var b []byte
	chunk := make([]byte, 4096)
	for len(b) < maxHeader {
		want := maxHeader - len(b)
		if want > len(chunk) {
			want = len(chunk)
		}
		part := make([]byte, want)
		n, err := r.ReadAt(part, int64(len(b)))
		b = append(b, part[:n]...)
		if end := bytes.Index(b, []byte("\n...\n")); end >= 0 {
			b = b[:end+5]
			break
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return header{}, err
		}
		if n == 0 {
			break
		}
	}
	if len(b) < 6 || !bytes.HasPrefix(b, []byte(Magic)) {
		return header{}, errors.New("not an ASDF file: missing #ASDF magic")
	}
	lineEnd := bytes.IndexByte(b, '\n')
	if lineEnd < 0 {
		return header{}, errors.New("ASDF header missing version line")
	}
	version := strings.TrimSpace(string(b[len(Magic):lineEnd]))
	standardLineStart := lineEnd + 1
	standardLineEnd := bytes.IndexByte(b[standardLineStart:], '\n')
	if standardLineEnd < 0 {
		return header{}, errors.New("ASDF header missing standard version line")
	}
	standardLineEnd += standardLineStart
	const standardPrefix = "#ASDF_STANDARD "
	standardLine := string(b[standardLineStart:standardLineEnd])
	if !strings.HasPrefix(standardLine, standardPrefix) {
		return header{}, errors.New("ASDF header missing #ASDF_STANDARD directive")
	}
	standard := strings.TrimSpace(strings.TrimPrefix(standardLine, standardPrefix))
	if standard == "" {
		return header{}, errors.New("ASDF header missing standard version")
	}
	end := bytes.Index(b, []byte("\n...\n"))
	if end < 0 {
		return header{}, errors.New("ASDF YAML header terminator not found")
	}
	yamlStart := lineEnd + 1
	y := b[yamlStart : end+5]
	var probe yaml.Node
	if err := yaml.Unmarshal(y, &probe); err != nil {
		return header{}, fmt.Errorf("invalid ASDF YAML header: %w", err)
	}
	return header{yaml: y, start: int64(end + 5), version: version, standard: standard}, nil
}

func scanBlocks(r io.ReaderAt, start int64) ([]Block, error) {
	info, ok := r.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return nil, errors.New("ASDF reader must provide file size")
	}
	st, err := info.Stat()
	if err != nil {
		return nil, err
	}
	if start < 0 || start > st.Size() {
		return nil, errors.New("invalid ASDF block start")
	}
	var out []Block
	var pos = start
	buf := make([]byte, 4)
	for pos+4 <= st.Size() {
		if _, err := r.ReadAt(buf, pos); err != nil {
			break
		}
		if binary.BigEndian.Uint32(buf) != blockMagic {
			pos++
			continue
		}
		pre := make([]byte, blockPreambleSize)
		if _, err := r.ReadAt(pre, pos); err != nil {
			return nil, fmt.Errorf("ASDF block header at %d: %w", pos, err)
		}
		headerSize := int64(binary.BigEndian.Uint16(pre[4:6]))
		if headerSize < blockRemainderSize || headerSize > st.Size()-pos-blockPreambleSize {
			return nil, fmt.Errorf("invalid ASDF block header size %d at offset %d", headerSize, pos)
		}
		if headerSize != blockRemainderSize {
			return nil, fmt.Errorf("unsupported ASDF block header size %d at offset %d", headerSize, pos)
		}
		h := make([]byte, blockPreambleSize+int(headerSize))
		copy(h, pre)
		if _, err := r.ReadAt(h[blockPreambleSize:], pos+blockPreambleSize); err != nil {
			return nil, fmt.Errorf("ASDF block header at %d: %w", pos, err)
		}
		flags := binary.BigEndian.Uint32(h[6:10])
		compression := h[10:14]
		allocated := int64(binary.BigEndian.Uint64(h[14:22]))
		size := int64(binary.BigEndian.Uint64(h[22:30]))
		dataSize := int64(binary.BigEndian.Uint64(h[30:38]))
		if !bytes.Equal(compression, []byte{0, 0, 0, 0}) {
			return nil, fmt.Errorf("unsupported ASDF block compression %x at offset %d", compression, pos)
		}
		if flags != 0 {
			return nil, fmt.Errorf("unsupported ASDF block flags 0x%x at offset %d", flags, pos)
		}
		if size < 0 || dataSize != size || allocated < size || allocated > st.Size()-pos-blockPreambleSize-headerSize {
			return nil, fmt.Errorf("invalid ASDF block size at offset %d", pos)
		}
		b := Block{Index: len(out), Offset: pos, PayloadOffset: pos + blockPreambleSize + headerSize, Size: size, Allocated: allocated, Flags: flags}
		copy(b.Compression[:], compression)
		copy(b.Checksum[:], h[38:54])
		out = append(out, b)
		pos += blockPreambleSize + headerSize + allocated
	}
	return out, nil
}

func validateTree(root *yaml.Node, d *Document) error {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return errors.New("ASDF YAML root is missing")
	}
	n := root.Content[0]
	if n.Kind != yaml.MappingNode {
		return errors.New("ASDF YAML root must be a mapping")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		role := key.Value
		if role != "data" && role != "dq" && role != "err" && role != "var_flat" && role != "var_poisson" && role != "var_rnoise" {
			continue
		}
		a, err := arrayFromNode(role, val)
		if err != nil {
			return err
		}
		if a.Source < 0 || a.Source >= len(d.Blocks) {
			return fmt.Errorf("ASDF %s source block %d is unavailable", role, a.Source)
		}
		a.Block = d.Blocks[a.Source]
		if a.Block.Size != expectedBytes(a) {
			return fmt.Errorf("ASDF %s block %d is %d bytes, need exactly %d for shape and datatype", role, a.Source, a.Block.Size, expectedBytes(a))
		}
		d.Arrays[role] = a
	}
	if _, ok := d.Arrays["data"]; !ok {
		return errors.New("JWST ASDF ImageModel missing data array")
	}
	return nil
}

func expectedBytes(a Array) int64 {
	if len(a.Shape) != 2 {
		return 0
	}
	bytesPerSample := int64(4)
	var count int64 = 1
	for _, dimension := range a.Shape {
		if dimension <= 0 || count > (1<<63-1)/dimension {
			return 1<<63 - 1
		}
		count *= dimension
	}
	if count > (1<<63-1)/bytesPerSample {
		return 1<<63 - 1
	}
	return count * bytesPerSample
}

func arrayFromNode(role string, n *yaml.Node) (Array, error) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return Array{}, fmt.Errorf("ASDF %s must be an ndarray descriptor", role)
	}
	a := Array{Role: role, Tag: n.Tag}
	if !strings.Contains(n.Tag, "ndarray") {
		return a, fmt.Errorf("ASDF %s has unsupported tag %q", role, n.Tag)
	}
	seen := make(map[string]bool, 4)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		switch k {
		case "source", "datatype", "byteorder", "shape":
			if seen[k] {
				return a, fmt.Errorf("ASDF %s descriptor repeats %s", role, k)
			}
			seen[k] = true
		}
		switch k {
		case "source":
			if v.Kind != yaml.ScalarNode || v.Tag != "!!int" {
				return a, fmt.Errorf("ASDF %s source must be a local block index", role)
			}
			if _, e := fmt.Sscan(v.Value, &a.Source); e != nil {
				return a, fmt.Errorf("ASDF %s source: %w", role, e)
			}
		case "datatype":
			a.DType = v.Value
		case "byteorder":
			a.ByteOrder = v.Value
		case "shape":
			if v.Kind != yaml.SequenceNode {
				return a, fmt.Errorf("ASDF %s shape must be a sequence", role)
			}
			for _, x := range v.Content {
				var z int64
				if x.Tag != "!!int" {
					return a, fmt.Errorf("ASDF %s shape contains invalid dimension", role)
				}
				if _, scanErr := fmt.Sscan(x.Value, &z); scanErr != nil || z <= 0 {
					return a, fmt.Errorf("ASDF %s shape contains invalid dimension", role)
				}
				if z > 1<<31 {
					return a, fmt.Errorf("ASDF %s dimension too large", role)
				}
				a.Shape = append(a.Shape, z)
			}
		case "compression":
			return a, fmt.Errorf("ASDF %s uses unsupported compression", role)
		case "offset", "strides", "mask", "inline":
			return a, fmt.Errorf("ASDF %s uses unsupported %s", role, k)
		}
	}
	for _, key := range []string{"source", "datatype", "byteorder", "shape"} {
		if !seen[key] {
			return a, fmt.Errorf("ASDF %s descriptor is missing %s", role, key)
		}
	}
	if len(a.Shape) != 2 {
		return a, fmt.Errorf("ASDF %s must be a 2-D array", role)
	}
	if a.DType != "float32" && role != "dq" {
		return a, fmt.Errorf("ASDF %s datatype %q is unsupported", role, a.DType)
	}
	if role == "dq" && a.DType != "uint32" {
		return a, fmt.Errorf("ASDF dq datatype %q is unsupported", a.DType)
	}
	if a.ByteOrder != "little" && a.ByteOrder != "=" && a.ByteOrder != "big" {
		return a, fmt.Errorf("ASDF %s byteorder %q is unsupported", role, a.ByteOrder)
	}
	if role == "dq" && a.ByteOrder != "little" {
		return a, fmt.Errorf("ASDF dq byteorder %q is unsupported", a.ByteOrder)
	}
	return a, nil
}

// ReadArray decodes one supported root array. It allocates only the requested
// array and validates the ASDF block checksum when a checksum is present.
func (d *Document) ReadArray(role string) ([]float32, []uint32, error) {
	a, ok := d.Arrays[role]
	if !ok {
		return nil, nil, fmt.Errorf("ASDF array %q is unavailable", role)
	}
	f, err := os.Open(d.Path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	payload := make([]byte, a.Block.Size)
	if _, err := f.ReadAt(payload, a.Block.PayloadOffset); err != nil {
		return nil, nil, fmt.Errorf("ASDF %s payload: %w", role, err)
	}
	if err := verifyChecksum(payload, a.Block.Checksum[:]); err != nil {
		return nil, nil, fmt.Errorf("ASDF %s checksum: %w", role, err)
	}
	return decodePayload(a, payload)
}

// StreamArray decodes one row at a time. The callback must consume its slices
// before returning; the buffers are reused on the next row.
func (d *Document) StreamArray(ctx context.Context, role string, fn func(int, []float32, []uint32) error) error {
	if fn == nil {
		return errors.New("ASDF row callback is required")
	}
	a, ok := d.Arrays[role]
	if !ok {
		return fmt.Errorf("ASDF array %q is unavailable", role)
	}
	f, err := os.Open(d.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(a.Block.PayloadOffset, io.SeekStart); err != nil {
		return err
	}
	width, height := int(a.Shape[1]), int(a.Shape[0])
	buf := make([]byte, width*4)
	rowArray := a
	rowArray.Shape = append([]int64(nil), a.Shape...)
	rowArray.Shape[0] = 1
	hash := md5.New()
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.ReadFull(f, buf); err != nil {
			return fmt.Errorf("ASDF %s payload row %d: %w", role, y, err)
		}
		if _, err := hash.Write(buf); err != nil {
			return err
		}
		floats, dqs, err := decodePayload(rowArray, buf)
		if err != nil {
			return err
		}
		if err := fn(y, floats, dqs); err != nil {
			return err
		}
	}
	if err := verifyDigest(hash.Sum(nil), a.Block.Checksum[:]); err != nil {
		return fmt.Errorf("ASDF %s checksum: %w", role, err)
	}
	return nil
}

func verifyChecksum(payload, checksum []byte) error {
	if checksumAbsent(checksum) {
		return nil
	}
	sum := md5.Sum(payload)
	return verifyDigest(sum[:], checksum)
}

func verifyDigest(digest, checksum []byte) error {
	if checksumAbsent(checksum) {
		return nil
	}
	if !bytes.Equal(digest, checksum) {
		return errors.New("block checksum mismatch")
	}
	return nil
}

func checksumAbsent(checksum []byte) bool {
	for _, b := range checksum {
		if b != 0 {
			return false
		}
	}
	return true
}

func decodePayload(a Array, payload []byte) ([]float32, []uint32, error) {
	n := int(a.Shape[0] * a.Shape[1])
	if len(payload) < n*4 {
		return nil, nil, io.ErrUnexpectedEOF
	}
	if a.Role == "dq" {
		out := make([]uint32, n)
		for i := range out {
			out[i] = binary.LittleEndian.Uint32(payload[i*4:])
		}
		return nil, out, nil
	}
	out := make([]float32, n)
	var order binary.ByteOrder = binary.LittleEndian
	if a.ByteOrder == "big" {
		order = binary.BigEndian
	}
	for i := range out {
		out[i] = math.Float32frombits(order.Uint32(payload[i*4:]))
	}
	return out, nil, nil
}
