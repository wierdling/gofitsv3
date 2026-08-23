package fitsio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// LoadSelectedHDU loads one image HDU and the primary header. An empty name
// selects the primary HDU; otherwise name and (when non-empty) EXTVER must
// match. Headers for other HDUs are scanned, but their pixel data is never
// decoded.
func LoadSelectedHDU(path, name, extver string) (Header, HDU, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, HDU{}, err
	}
	defer f.Close()

	var primary Header
	var selected *HDU
	var pos int64
	index := 0
	for {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return Header{}, HDU{}, err
		}
		hdr, headerBytes, err := readHeader(bufio.NewReaderSize(f, 64*1024))
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Header{}, HDU{}, err
		}
		if index == 0 {
			primary = hdr
		}
		pos += int64(headerBytes)
		extName := HeaderString(hdr, "EXTNAME")
		nameMatch := strings.EqualFold(extName, name)
		if strings.EqualFold(name, "SCI") {
			nameMatch = strings.HasPrefix(strings.ToUpper(strings.TrimSpace(extName)), "SCI")
		}
		match := (name == "" && index == 0) || (name != "" && nameMatch && (extver == "" || HeaderString(hdr, "EXTVER") == strings.TrimSpace(extver)))
		dataBytes := imageDataBytes(hdr)
		if parseInt(hdr.Cards["NAXIS"]) >= 2 && dataBytes == 0 {
			return Header{}, HDU{}, fmt.Errorf("unsupported BITPIX %d", parseInt(hdr.Cards["BITPIX"]))
		}
		if match {
			if _, err := f.Seek(pos, io.SeekStart); err != nil {
				return Header{}, HDU{}, err
			}
			got, _, err := readImage(bufio.NewReaderSize(f, 64*1024), hdr)
			if err != nil {
				return Header{}, HDU{}, err
			}
			selected = &got
			break
		}
		pos += int64(dataBytes + padding(dataBytes))
		index++
	}
	if selected == nil {
		return primary, HDU{}, fmt.Errorf("HDU %q (EXTVER %q) not found in %s", name, extver, path)
	}
	return primary, *selected, nil
}

// InspectSelectedHDU returns the selected image metadata without decoding its
// pixel payload. It is intended for disk-backed callers that keep the raster
// in a Float32Artifact and only need dimensions and headers in memory.
func InspectSelectedHDU(path, name, extver string) (Header, HDU, error) {
	primary, hdr, _, err := locateSelectedImage(path, name, extver)
	if err != nil {
		return Header{}, HDU{}, err
	}
	w, h := parseInt(hdr.Cards["NAXIS1"]), parseInt(hdr.Cards["NAXIS2"])
	if w <= 0 || h <= 0 {
		return Header{}, HDU{}, errors.New("selected HDU is not a supported image")
	}
	return primary, HDU{Header: hdr, Data: ImageData{Width: w, Height: h}}, nil
}

// CopySelectedHDUToFloat32Artifact streams one selected image HDU into a
// normalized float32 artifact. It makes two bounded-memory passes over the
// selected data (for min/max and output), and never decodes another HDU.
func CopySelectedHDUToFloat32Artifact(srcPath, name, extver, dstPath string) error {
	return copySelectedHDUToFloat32Artifact(srcPath, name, extver, dstPath, true)
}

// CopySelectedHDUToRawFloat32Artifact streams decoded FITS sample values to
// an artifact without applying a per-channel min/max normalization.
func CopySelectedHDUToRawFloat32Artifact(srcPath, name, extver, dstPath string) error {
	return copySelectedHDUToFloat32Artifact(srcPath, name, extver, dstPath, false)
}

func copySelectedHDUToFloat32Artifact(srcPath, name, extver, dstPath string, normalize bool) error {
	primary, hdr, dataOffset, err := locateSelectedImage(srcPath, name, extver)
	_ = primary
	if err != nil {
		return err
	}
	width, height := parseInt(hdr.Cards["NAXIS1"]), parseInt(hdr.Cards["NAXIS2"])
	if width <= 0 || height <= 0 || imageDataBytes(hdr) == 0 {
		return errors.New("selected HDU is not a supported image")
	}
	min, max, err := scanSelectedRows(srcPath, dataOffset, hdr, false, nil)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dstPath), "."+filepath.Base(dstPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)
	a, err := CreateFloat32Artifact(tmpPath, width, height)
	if err != nil {
		return err
	}
	span := max - min
	if span == 0 {
		span = 1
	}
	row := make([]float32, width)
	_, _, err = scanSelectedRows(srcPath, dataOffset, hdr, true, func(y int, values []float32) error {
		for i, value := range values {
			if normalize {
				row[i] = (value - min) / span
			} else {
				row[i] = value
			}
		}
		return a.WriteRow(y, row)
	})
	if err != nil {
		_ = a.Close()
		return err
	}
	if err := a.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return err
	}
	return nil
}

func locateSelectedImage(path, name, extver string) (Header, Header, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, Header{}, 0, err
	}
	defer f.Close()
	var primary Header
	var pos int64
	for index := 0; ; index++ {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return Header{}, Header{}, 0, err
		}
		hdr, n, err := readHeader(bufio.NewReaderSize(f, 64*1024))
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Header{}, Header{}, 0, err
		}
		if index == 0 {
			primary = hdr
		}
		dataOffset := pos + int64(n)
		extName := HeaderString(hdr, "EXTNAME")
		nameMatch := strings.EqualFold(extName, name)
		if strings.EqualFold(name, "SCI") {
			nameMatch = strings.HasPrefix(strings.ToUpper(strings.TrimSpace(extName)), "SCI")
		}
		match := (name == "" && index == 0) || (name != "" && nameMatch && (extver == "" || HeaderString(hdr, "EXTVER") == strings.TrimSpace(extver)))
		if match {
			return primary, hdr, dataOffset, nil
		}
		data := imageDataBytes(hdr)
		if parseInt(hdr.Cards["NAXIS"]) >= 2 && data == 0 {
			return Header{}, Header{}, 0, fmt.Errorf("unsupported BITPIX %d", parseInt(hdr.Cards["BITPIX"]))
		}
		pos = dataOffset + int64(data+padding(data))
	}
	return primary, Header{}, 0, fmt.Errorf("HDU %q (EXTVER %q) not found in %s", name, extver, path)
}

func scanSelectedRows(path string, offset int64, hdr Header, write bool, each func(int, []float32) error) (float32, float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, 0, err
	}
	width, height := parseInt(hdr.Cards["NAXIS1"]), parseInt(hdr.Cards["NAXIS2"])
	bpp := imageDataBytes(hdr) / (width * height)
	if bpp <= 0 {
		return 0, 0, errors.New("unsupported image data")
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	rowBytes := width * bpp
	raw := make([]byte, rowBytes)
	values := make([]float32, width)
	min, max := float32(math.Inf(1)), float32(math.Inf(-1))
	for y := 0; y < height; y++ {
		if _, err := io.ReadFull(reader, raw); err != nil {
			return 0, 0, err
		}
		decodeRow(values, raw, parseInt(hdr.Cards["BITPIX"]))
		for _, value := range values {
			if value < min {
				min = value
			}
			if value > max {
				max = value
			}
		}
		if write && each != nil {
			if err := each(y, values); err != nil {
				return 0, 0, err
			}
		}
	}
	return min, max, nil
}

func decodeRow(dst []float32, raw []byte, bitpix int) {
	for i := range dst {
		switch bitpix {
		case 8:
			dst[i] = float32(raw[i])
		case 16:
			dst[i] = float32(int16(binary.BigEndian.Uint16(raw[i*2:])))
		case 32:
			dst[i] = float32(int32(binary.BigEndian.Uint32(raw[i*4:])))
		case -32:
			dst[i] = math.Float32frombits(binary.BigEndian.Uint32(raw[i*4:]))
		case -64:
			dst[i] = float32(math.Float64frombits(binary.BigEndian.Uint64(raw[i*8:])))
		}
	}
}

const artifactHeaderSize int64 = 16
const maxArtifactRowBytes uint64 = 64 << 20

var artifactMagic = [8]byte{'G', 'F', '3', '2', 'A', 'R', 'T', '1'}

// Float32Artifact is a compact little-endian float32 raster. Its header is
// fixed-size so rows can be read or replaced with bounded memory.
type Float32Artifact struct {
	f      *os.File
	Width  int
	Height int
	mu     sync.Mutex
	buf    []byte
}

// ArtifactInstrumentation records bounded-access activity for deterministic
// tests. It is nil by default and has no production overhead beyond a nil check.
type ArtifactInstrumentation struct {
	OpenArtifacts         atomic.Int64
	MaxOpenArtifacts      atomic.Int64
	MaterializedPlanes    atomic.Int64
	MaxMaterializedPlanes atomic.Int64
	LargestBuffer         atomic.Int64
}

var artifactInstrumentation atomic.Pointer[ArtifactInstrumentation]

func SetArtifactInstrumentation(i *ArtifactInstrumentation) { artifactInstrumentation.Store(i) }

func recordArtifactOpen(delta int64) {
	if i := artifactInstrumentation.Load(); i != nil {
		n := i.OpenArtifacts.Add(delta)
		for {
			m := i.MaxOpenArtifacts.Load()
			if n <= m || i.MaxOpenArtifacts.CompareAndSwap(m, n) {
				break
			}
		}
	}
}
func recordBuffer(n int) {
	if i := artifactInstrumentation.Load(); i != nil {
		for {
			m := i.LargestBuffer.Load()
			if int64(n) <= m || i.LargestBuffer.CompareAndSwap(m, int64(n)) {
				break
			}
		}
	}
}

func CreateFloat32Artifact(path string, width, height int) (*Float32Artifact, error) {
	if _, err := artifactByteSize(width, height); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	a := &Float32Artifact{f: f, Width: width, Height: height}
	if err := a.writeHeader(); err != nil {
		_ = f.Close()
		return nil, err
	}
	a.buf = make([]byte, width*4)
	recordArtifactOpen(1)
	return a, nil
}

func artifactByteSize(width, height int) (int64, error) {
	const maxUint32 = uint64(^uint32(0))
	if width <= 0 || height <= 0 || uint64(width) > maxUint32 || uint64(height) > maxUint32 {
		return 0, fmt.Errorf("invalid artifact dimensions %dx%d", width, height)
	}
	w, h := uint64(width), uint64(height)
	if w > maxArtifactRowBytes/4 {
		return 0, errors.New("artifact row is too large")
	}
	const maxInt64 = uint64(^uint64(0) >> 1)
	if w > (maxInt64-uint64(artifactHeaderSize))/4/h {
		return 0, errors.New("artifact dimensions are too large")
	}
	return artifactHeaderSize + int64(w*h*4), nil
}

func OpenFloat32Artifact(path string) (*Float32Artifact, error) {
	return openFloat32Artifact(path, false)
}

// OpenFloat32ArtifactReadOnly opens an artifact without write permissions.
func OpenFloat32ArtifactReadOnly(path string) (*Float32Artifact, error) {
	return openFloat32Artifact(path, true)
}

func openFloat32Artifact(path string, readOnly bool) (*Float32Artifact, error) {
	flag := os.O_RDWR
	if readOnly {
		flag = os.O_RDONLY
	}
	f, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		return nil, err
	}
	var h [artifactHeaderSize]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		_ = f.Close()
		return nil, err
	}
	if string(h[:8]) != string(artifactMagic[:]) {
		_ = f.Close()
		return nil, errors.New("invalid float32 artifact header")
	}
	w, height := int(binary.LittleEndian.Uint32(h[8:12])), int(binary.LittleEndian.Uint32(h[12:16]))
	size, sizeErr := artifactByteSize(w, height)
	if sizeErr != nil {
		_ = f.Close()
		return nil, sizeErr
	}
	info, err := f.Stat()
	if err != nil || info.Size() < size {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("truncated float32 artifact")
	}
	a := &Float32Artifact{f: f, Width: w, Height: height, buf: make([]byte, w*4)}
	recordArtifactOpen(1)
	return a, nil
}

func (a *Float32Artifact) writeHeader() error {
	var h [artifactHeaderSize]byte
	copy(h[:8], artifactMagic[:])
	binary.LittleEndian.PutUint32(h[8:12], uint32(a.Width))
	binary.LittleEndian.PutUint32(h[12:16], uint32(a.Height))
	_, err := a.f.WriteAt(h[:], 0)
	return err
}

func (a *Float32Artifact) ReadRow(row int, dst []float32) error {
	if a == nil || a.f == nil {
		return errors.New("float32 artifact is closed")
	}
	if row < 0 || row >= a.Height || len(dst) < a.Width {
		return fmt.Errorf("row %d or destination length is out of range", row)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if cap(a.buf) < a.Width*4 {
		a.buf = make([]byte, a.Width*4)
	}
	buf := a.buf[:a.Width*4]
	recordBuffer(len(buf))
	if _, err := a.f.ReadAt(buf, artifactHeaderSize+int64(row*a.Width*4)); err != nil {
		return err
	}
	for i := 0; i < a.Width; i++ {
		dst[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return nil
}

// ReadRows reads count contiguous full rows starting at row into dst.
func (a *Float32Artifact) ReadRows(row, count int, dst []float32) error {
	if a == nil || a.f == nil {
		return errors.New("float32 artifact is closed")
	}
	if row < 0 || count < 0 || row > a.Height-count || len(dst) < count*a.Width {
		return errors.New("row range is out of bounds")
	}
	n := count * a.Width * 4
	a.mu.Lock()
	defer a.mu.Unlock()
	if cap(a.buf) < n {
		a.buf = make([]byte, n)
	}
	buf := a.buf[:n]
	recordBuffer(len(buf))
	if _, err := a.f.ReadAt(buf, artifactHeaderSize+int64(row*a.Width*4)); err != nil {
		return err
	}
	for i := 0; i < count*a.Width; i++ {
		dst[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return nil
}

// ReadRange reads the half-open x range from one row into dst.
func (a *Float32Artifact) ReadRange(row, x0, x1 int, dst []float32) error {
	if a == nil || a.f == nil {
		return errors.New("float32 artifact is closed")
	}
	if row < 0 || row >= a.Height || x0 < 0 || x1 < x0 || x1 > a.Width || len(dst) < x1-x0 {
		return errors.New("row range is out of bounds")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	n := (x1 - x0) * 4
	if cap(a.buf) < n {
		a.buf = make([]byte, n)
	}
	buf := a.buf[:n]
	recordBuffer(len(buf))
	if _, err := a.f.ReadAt(buf, artifactHeaderSize+int64((row*a.Width+x0)*4)); err != nil {
		return err
	}
	for i := 0; i < x1-x0; i++ {
		dst[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return nil
}

// ReadTile reads a bounded rectangle row-major into dst.
func (a *Float32Artifact) ReadTile(x0, y0, x1, y1 int, dst []float32) error {
	if x0 < 0 || y0 < 0 || x1 < x0 || y1 < y0 || x1 > a.Width || y1 > a.Height || len(dst) < (x1-x0)*(y1-y0) {
		return errors.New("tile is out of bounds")
	}
	for y := y0; y < y1; y++ {
		if err := a.ReadRange(y, x0, x1, dst[(y-y0)*(x1-x0):]); err != nil {
			return err
		}
	}
	return nil
}

// ReadRowRotatedCW reads one row of the clockwise-rotated raster. The rotated
// raster has dimensions Height x Width and the caller supplies Height values.
func (a *Float32Artifact) ReadRowRotatedCW(row int, dst []float32) error {
	if a == nil || a.f == nil {
		return errors.New("float32 artifact is closed")
	}
	if row < 0 || row >= a.Width || len(dst) < a.Height {
		return errors.New("rotated row is out of bounds")
	}
	buf := make([]float32, a.Width)
	for x := 0; x < a.Height; x++ {
		if err := a.ReadRow(a.Height-1-x, buf); err != nil {
			return err
		}
		dst[x] = buf[row]
	}
	return nil
}

func (a *Float32Artifact) WriteRow(row int, src []float32) error {
	if a == nil || a.f == nil {
		return errors.New("float32 artifact is closed")
	}
	if row < 0 || row >= a.Height || len(src) < a.Width {
		return fmt.Errorf("row %d or source length is out of range", row)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if cap(a.buf) < a.Width*4 {
		a.buf = make([]byte, a.Width*4)
	}
	buf := a.buf[:a.Width*4]
	recordBuffer(len(buf))
	for i := 0; i < a.Width; i++ {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(src[i]))
	}
	_, err := a.f.WriteAt(buf, artifactHeaderSize+int64(row*a.Width*4))
	return err
}

// WriteRows writes count contiguous full rows starting at row from src.
func (a *Float32Artifact) WriteRows(row, count int, src []float32) error {
	if a == nil || a.f == nil {
		return errors.New("float32 artifact is closed")
	}
	if row < 0 || count < 0 || row > a.Height-count || len(src) < count*a.Width {
		return errors.New("row range is out of bounds")
	}
	n := count * a.Width * 4
	a.mu.Lock()
	defer a.mu.Unlock()
	if cap(a.buf) < n {
		a.buf = make([]byte, n)
	}
	buf := a.buf[:n]
	recordBuffer(len(buf))
	for i := 0; i < count*a.Width; i++ {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(src[i]))
	}
	_, err := a.f.WriteAt(buf, artifactHeaderSize+int64(row*a.Width*4))
	return err
}

func (a *Float32Artifact) Close() error {
	if a == nil || a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	recordArtifactOpen(-1)
	return err
}

// Sync flushes all artifact writes to the underlying file. Transactions use
// this before exposing staged paths to bounded readers during preparation.
func (a *Float32Artifact) Sync() error {
	if a == nil || a.f == nil {
		return errors.New("float32 artifact is closed")
	}
	return a.f.Sync()
}

// Float32ArtifactTransaction writes a sibling artifact and atomically replaces
// the destination only after Commit succeeds.
type Float32ArtifactTransaction struct {
	artifact           *Float32Artifact
	tmpPath, finalPath string
	done               bool
}

func BeginFloat32ArtifactTransaction(finalPath string, width, height int) (*Float32ArtifactTransaction, error) {
	if _, err := artifactByteSize(width, height); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(finalPath), "."+filepath.Base(finalPath)+".tmp-*")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	if err = f.Close(); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	a, err := CreateFloat32Artifact(tmp, width, height)
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return &Float32ArtifactTransaction{artifact: a, tmpPath: tmp, finalPath: finalPath}, nil
}
func (t *Float32ArtifactTransaction) Artifact() *Float32Artifact {
	if t == nil {
		return nil
	}
	return t.artifact
}

// StagedPath returns the temporary path containing the transaction's complete
// artifact before commit. It is intended only for pre-commit validation.
func (t *Float32ArtifactTransaction) StagedPath() string {
	if t == nil {
		return ""
	}
	return t.tmpPath
}
func (t *Float32ArtifactTransaction) Abort() error {
	if t == nil || t.done {
		return nil
	}
	t.done = true
	if t.artifact != nil {
		_ = t.artifact.Close()
	}
	return os.Remove(t.tmpPath)
}
func (t *Float32ArtifactTransaction) Commit() error {
	if t == nil || t.done {
		return errors.New("artifact transaction is closed")
	}
	if err := t.artifact.Sync(); err != nil {
		_ = t.artifact.Close()
		_ = os.Remove(t.tmpPath)
		t.done = true
		return err
	}
	if err := t.artifact.Close(); err != nil {
		_ = os.Remove(t.tmpPath)
		t.done = true
		return err
	}
	if err := atomicReplaceArtifact(t.tmpPath, t.finalPath); err != nil {
		_ = os.Remove(t.tmpPath)
		t.done = true
		return err
	}
	t.done = true
	return nil
}

func atomicReplaceArtifact(tmpPath, finalPath string) error {
	if err := os.Rename(tmpPath, finalPath); err == nil {
		return nil
	}
	// Windows does not rename over an existing file. Keep a rollback copy so a
	// failed replacement never leaves the destination missing or partial.
	backup := finalPath + ".replace-backup"
	_ = os.Remove(backup)
	if err := os.Rename(finalPath, backup); err != nil {
		if os.IsNotExist(err) {
			return os.Rename(tmpPath, finalPath)
		}
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Rename(backup, finalPath)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func CopyFloat32Artifact(srcPath, dstPath string) error {
	s, err := OpenFloat32ArtifactReadOnly(srcPath)
	if err != nil {
		return err
	}
	defer s.Close()
	t, err := BeginFloat32ArtifactTransaction(dstPath, s.Width, s.Height)
	if err != nil {
		return err
	}
	row := make([]float32, s.Width)
	for y := 0; y < s.Height; y++ {
		if err := s.ReadRow(y, row); err != nil {
			_ = t.Abort()
			return err
		}
		if err := t.Artifact().WriteRow(y, row); err != nil {
			_ = t.Abort()
			return err
		}
	}
	return t.Commit()
}

// MaterializeFloat32Artifact loads one artifact plane for algorithms that
// cannot yet operate on rows. The caller owns the returned slice and should
// release it promptly; instrumentation tracks the bounded lifetime.
func MaterializeFloat32Artifact(path string) ([]float32, int, int, error) {
	lease, err := MaterializeFloat32ArtifactLease(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer lease.Release()
	return lease.Pixels, lease.Width, lease.Height, nil
}

// MaterializedPlane is a managed one-plane lease. Release it as soon as the
// operation consuming Pixels completes so instrumentation and memory lifetime
// remain deterministic.
type MaterializedPlane struct {
	Pixels        []float32
	Width, Height int
	release       func()
}

func (p *MaterializedPlane) Release() {
	if p != nil && p.release != nil {
		p.release()
		p.release = nil
		p.Pixels = nil
	}
}

func MaterializeFloat32ArtifactLease(path string) (*MaterializedPlane, error) {
	a, err := OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	plane := make([]float32, a.Width*a.Height)
	var release func()
	if i := artifactInstrumentation.Load(); i != nil {
		n := i.MaterializedPlanes.Add(1)
		for {
			m := i.MaxMaterializedPlanes.Load()
			if n <= m || i.MaxMaterializedPlanes.CompareAndSwap(m, n) {
				break
			}
		}
		release = func() { i.MaterializedPlanes.Add(-1) }
	}
	row := make([]float32, a.Width)
	for y := 0; y < a.Height; y++ {
		if err := a.ReadRow(y, row); err != nil {
			if release != nil {
				release()
			}
			return nil, err
		}
		copy(plane[y*a.Width:], row)
	}
	return &MaterializedPlane{Pixels: plane, Width: a.Width, Height: a.Height, release: release}, nil
}

// WriteNormalizedFloat32Artifact writes normalized [0,1] pixels using bounded
// row buffers. The source image itself remains in memory for compatibility
// with existing callers; CopySelectedHDUToFloat32Artifact streams FITS input.
func WriteNormalizedFloat32Artifact(path string, img ImageData) error {
	a, err := CreateFloat32Artifact(path, img.Width, img.Height)
	if err != nil {
		return err
	}
	defer a.Close()
	norm := img.Normalize()
	row := make([]float32, img.Width)
	for y := 0; y < img.Height; y++ {
		copy(row, norm.Pixels[y*img.Width:(y+1)*img.Width])
		if err := a.WriteRow(y, row); err != nil {
			return err
		}
	}
	return nil
}
