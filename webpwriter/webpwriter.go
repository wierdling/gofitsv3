// Package webpwriter provides a dependency-free WebP lossless writer tuned for
// large, many-color RGBA images.
//
// This version always uses the compressed VP8L path. It is intended for large
// astronomical/technical images where indexed-color compression is not useful.
// It uses:
//   - sampled predictor selection for true-color images
//   - optional sampled subtract-green selection
//   - optional color-cache use only when the sampled residuals justify it
//   - canonical prefix/Huffman codes built from the encoded image data
//   - LZ77 backward references using a bounded hash-chain search
//   - no external packages
//
// The output is lossless. It will usually be larger than files produced by
// Google's cwebp encoder, but it avoids the very slow brute-force "try every
// mode" behavior and skips low-color indexed-image paths.
package webpwriter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"sort"
)

const maxVP8LDimension = 16384

// WriteRGBAWebPLosslessFile writes an RGBA byte buffer to a WebP lossless file.
//
// The rgba buffer must be in R, G, B, A byte order, with 4 bytes per pixel.
func WriteRGBAWebPLosslessFile(path string, rgba []byte, width, height int) error {
	data, err := EncodeRGBAWebPLossless(rgba, width, height)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// EncodeRGBAWebPLossless encodes an RGBA byte buffer into a complete WebP file.
//
// The returned bytes include the RIFF/WEBP container and the VP8L chunk.
func EncodeRGBAWebPLossless(rgba []byte, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid image size %dx%d", width, height)
	}
	if width > maxVP8LDimension || height > maxVP8LDimension {
		return nil, fmt.Errorf("webp lossless max dimension is %dx%d, got %dx%d",
			maxVP8LDimension, maxVP8LDimension, width, height)
	}

	need := width * height * 4
	if len(rgba) < need {
		return nil, fmt.Errorf("rgba buffer too small: got %d bytes, need %d", len(rgba), need)
	}
	rgba = rgba[:need]

	// Large true-color images are expensive to encode repeatedly. Instead of
	// brute-forcing many complete encodes, sample the image, select the most
	// promising predictor/subtract-green/cache setup, then perform one full
	// compressed VP8L encode.
	//
	// This intentionally skips the indexed-color transform because these images
	// are expected to have many colors.
	opts := chooseLargeTrueColorOptions(rgba, width, height)
	vp8l, err := encodeVP8LAdvanced(rgba, width, height, opts)
	if err != nil {
		return nil, err
	}

	return wrapRIFFVP8L(vp8l), nil
}

// WriteImageWebPLosslessFile converts any image.Image to RGBA and writes it as
// a WebP lossless file.
func WriteImageWebPLosslessFile(path string, img image.Image) error {
	rgba, width, height, err := imageToRGBA(img)
	if err != nil {
		return err
	}

	return WriteRGBAWebPLosslessFile(path, rgba, width, height)
}

func imageToRGBA(img image.Image) ([]byte, int, int, error) {
	if img == nil {
		return nil, 0, 0, errors.New("nil image")
	}

	b := img.Bounds()
	width := b.Dx()
	height := b.Dy()

	if width <= 0 || height <= 0 {
		return nil, 0, 0, fmt.Errorf("invalid image bounds %v", b)
	}

	out := make([]byte, width*height*4)

	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)

			out[i+0] = c.R
			out[i+1] = c.G
			out[i+2] = c.B
			out[i+3] = c.A

			i += 4
		}
	}

	return out, width, height, nil
}

func wrapRIFFVP8L(vp8l []byte) []byte {
	paddedSize := len(vp8l)
	if paddedSize%2 != 0 {
		paddedSize++
	}
	riffSize := uint32(4 + 8 + paddedSize)

	out := make([]byte, 0, 12+8+paddedSize)
	out = append(out, 'R', 'I', 'F', 'F')
	out = appendU32LE(out, riffSize)
	out = append(out, 'W', 'E', 'B', 'P')
	out = append(out, 'V', 'P', '8', 'L')
	out = appendU32LE(out, uint32(len(vp8l)))
	out = append(out, vp8l...)
	if len(vp8l)%2 != 0 {
		out = append(out, 0)
	}
	return out
}

func appendU32LE(dst []byte, v uint32) []byte {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	return append(dst, buf[:]...)
}

type advancedOptions struct {
	subtractGreen  bool
	predictorMode  int
	colorCacheBits int
}

// chooseLargeTrueColorOptions picks one compressed VP8L strategy from a small
// sample of the image. It avoids running many full encodes, which is important
// for 4K/8K astronomical images.
func chooseLargeTrueColorOptions(rgba []byte, width, height int) advancedOptions {
	modes := []int{12, 11, 13, 7, 2, 1}

	best := advancedOptions{predictorMode: 12}
	bestScore := math.Inf(1)

	for _, subtractGreen := range []bool{false, true} {
		for _, mode := range modes {
			score, repeatRatio := estimateResidualEntropy(rgba, width, height, subtractGreen, mode)
			if score < bestScore {
				bestScore = score
				best = advancedOptions{subtractGreen: subtractGreen, predictorMode: mode}

				// Color cache helps when exact residual pixels repeat a lot. For
				// large many-color images it often does not help, so enable it only
				// when the sampled residuals strongly suggest repeated values.
				if repeatRatio >= 0.15 {
					best.colorCacheBits = 8
				}
			}
		}
	}

	return best
}

func estimateResidualEntropy(rgba []byte, width, height int, subtractGreen bool, predictorMode int) (bitsPerPixel float64, repeatRatio float64) {
	pixelCount := width * height
	if pixelCount == 0 {
		return math.Inf(1), 0
	}

	const maxSamples = 262144
	step := pixelCount / maxSamples
	if step < 1 {
		step = 1
	}

	var histA, histR, histG, histB [256]int
	repeats := make(map[uint32]int, 4096)
	samples := 0

	for idx := 0; idx < pixelCount; idx += step {
		x := idx % width
		y := idx / width

		p := transformedPixelForEstimate(rgbaPixelAt(rgba, idx), subtractGreen)
		pred := predictorForEstimatedPixel(rgba, width, height, x, y, subtractGreen, predictorMode)
		residual := subARGB(p, pred)

		histA[argbA(residual)]++
		histR[argbR(residual)]++
		histG[argbG(residual)]++
		histB[argbB(residual)]++
		repeats[residual]++
		samples++
	}

	if samples == 0 {
		return math.Inf(1), 0
	}

	bits := entropyBits(histA[:], samples) + entropyBits(histR[:], samples) + entropyBits(histG[:], samples) + entropyBits(histB[:], samples)

	mostCommon := 0
	for _, count := range repeats {
		if count > mostCommon {
			mostCommon = count
		}
	}

	return bits / float64(samples), float64(mostCommon) / float64(samples)
}

func entropyBits(hist []int, total int) float64 {
	if total <= 0 {
		return 0
	}

	bits := 0.0
	for _, count := range hist {
		if count == 0 {
			continue
		}
		p := float64(count) / float64(total)
		bits += float64(count) * -math.Log2(p)
	}
	return bits
}

func transformedPixelForEstimate(p uint32, subtractGreen bool) uint32 {
	if !subtractGreen {
		return p
	}
	g := argbG(p)
	return packARGB(argbA(p), argbR(p)-g, g, argbB(p)-g)
}
func rgbaPixelAt(rgba []byte, pixelIndex int) uint32 {
	i := pixelIndex * 4
	return packARGB(rgba[i+3], rgba[i+0], rgba[i+1], rgba[i+2])
}

func predictorForEstimatedPixel(rgba []byte, width, height, x, y int, subtractGreen bool, mode int) uint32 {
	_ = height
	get := func(px, py int) uint32 {
		return transformedPixelForEstimate(rgbaPixelAt(rgba, py*width+px), subtractGreen)
	}

	if x == 0 && y == 0 {
		return packARGB(255, 0, 0, 0)
	}
	if y == 0 {
		return get(x-1, y)
	}
	if x == 0 {
		return get(x, y-1)
	}

	l := get(x-1, y)
	t := get(x, y-1)
	tl := get(x-1, y-1)

	switch mode {
	case 1:
		return l
	case 2:
		return t
	case 7:
		return avgARGB(l, t)
	case 11:
		return selectPredictor(l, t, tl)
	case 12:
		return clampAddSubtractFullARGB(l, t, tl)
	case 13:
		return clampAddSubtractHalfARGB(avgARGB(l, t), tl)
	default:
		return clampAddSubtractFullARGB(l, t, tl)
	}
}

func encodeVP8LAdvanced(rgba []byte, width, height int, opts advancedOptions) ([]byte, error) {
	if opts.predictorMode < 0 || opts.predictorMode > 13 {
		return nil, fmt.Errorf("invalid predictor mode %d", opts.predictorMode)
	}
	if opts.colorCacheBits != 0 && (opts.colorCacheBits < 1 || opts.colorCacheBits > 11) {
		return nil, fmt.Errorf("invalid color cache bits %d", opts.colorCacheBits)
	}

	pixels := rgbaToARGB(rgba)
	if opts.subtractGreen {
		applySubtractGreenTransform(pixels)
	}

	usePredictor := opts.predictorMode > 0
	if usePredictor {
		pixels = predictorResiduals(pixels, width, height, opts.predictorMode)
	}

	var bw bitWriter
	writeVP8LHeader(&bw, rgba, width, height)

	if opts.subtractGreen {
		bw.writeBits(1, 1)
		bw.writeBits(2, 2)
	}

	if usePredictor {
		bw.writeBits(1, 1)
		bw.writeBits(0, 2)
		sizeBits := 9
		bw.writeBits(uint32(sizeBits-2), 3)

		tw := divRoundUp(width, 1<<sizeBits)
		th := divRoundUp(height, 1<<sizeBits)
		predMeta := make([]uint32, tw*th)
		metaPixel := packARGB(255, 0, byte(opts.predictorMode), 0)
		for i := range predMeta {
			predMeta[i] = metaPixel
		}
		writeEntropyCodedImage(&bw, predMeta, tw, th, 0)
	}

	bw.writeBits(0, 1)
	writeSpatiallyCodedImage(&bw, pixels, width, height, opts.colorCacheBits)
	return bw.bytes(), nil
}

func encodeVP8LColorIndexed(rgba []byte, width, height int) ([]byte, error) {
	type colorStat struct {
		argb uint32
		freq int
	}
	pixels := rgbaToARGB(rgba)
	counts := make(map[uint32]int)
	for _, p := range pixels {
		counts[p]++
		if len(counts) > 256 {
			return nil, nil
		}
	}
	if len(counts) == 0 || len(counts) > 256 {
		return nil, nil
	}

	stats := make([]colorStat, 0, len(counts))
	for p, f := range counts {
		stats = append(stats, colorStat{argb: p, freq: f})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].freq != stats[j].freq {
			return stats[i].freq > stats[j].freq
		}
		return stats[i].argb < stats[j].argb
	})

	indexByColor := make(map[uint32]byte, len(stats))
	palette := make([]uint32, len(stats))
	for i, s := range stats {
		indexByColor[s.argb] = byte(i)
		palette[i] = s.argb
	}

	widthBits := 0
	switch {
	case len(palette) <= 2:
		widthBits = 3
	case len(palette) <= 4:
		widthBits = 2
	case len(palette) <= 16:
		widthBits = 1
	}

	packedWidth := divRoundUp(width, 1<<widthBits)
	indexed := make([]uint32, packedWidth*height)
	for y := 0; y < height; y++ {
		for x := 0; x < packedWidth; x++ {
			var g byte
			for k := 0; k < (1 << widthBits); k++ {
				srcX := x*(1<<widthBits) + k
				if srcX >= width {
					break
				}
				idx := indexByColor[pixels[y*width+srcX]]
				if widthBits == 0 {
					g = idx
				} else {
					g |= idx << uint(k*(8>>widthBits))
				}
			}
			indexed[y*packedWidth+x] = packARGB(255, 0, g, 0)
		}
	}

	paletteDeltas := make([]uint32, len(palette))
	var prev uint32
	for i, p := range palette {
		paletteDeltas[i] = subARGB(p, prev)
		prev = p
	}

	var bw bitWriter
	writeVP8LHeader(&bw, rgba, width, height)
	bw.writeBits(1, 1)
	bw.writeBits(3, 2)
	bw.writeBits(uint32(len(palette)-1), 8)
	writeEntropyCodedImage(&bw, paletteDeltas, len(palette), 1, 0)
	bw.writeBits(0, 1)
	writeSpatiallyCodedImage(&bw, indexed, packedWidth, height, 0)
	return bw.bytes(), nil
}

func encodeVP8LLiteralOnly(rgba []byte, width, height int) ([]byte, error) {
	var bw bitWriter
	writeVP8LHeader(&bw, rgba, width, height)
	bw.writeBits(0, 1) // no transforms
	bw.writeBits(0, 1) // no color cache
	bw.writeBits(0, 1) // no meta prefix codes
	writeAllByteSymbolsPrefixCode(&bw)
	writeAllByteSymbolsPrefixCode(&bw)
	writeAllByteSymbolsPrefixCode(&bw)
	writeAllByteSymbolsPrefixCode(&bw)
	writeSingleSymbolPrefixCode(&bw, 0)

	for i := 0; i < len(rgba); i += 4 {
		r := rgba[i+0]
		g := rgba[i+1]
		b := rgba[i+2]
		a := rgba[i+3]
		writeFixed8Symbol(&bw, g)
		writeFixed8Symbol(&bw, r)
		writeFixed8Symbol(&bw, b)
		writeFixed8Symbol(&bw, a)
	}
	return bw.bytes(), nil
}

const (
	vp8lLengthCodeCount       = 24
	vp8lBaseGreenAlphabetSize = 256 + vp8lLengthCodeCount
	vp8lByteAlphabetSize      = 256
	vp8lDistanceAlphabetSize  = 40
	vp8lMaxBackwardLength     = 4096
	vp8lLZ77MinLength         = 4
	vp8lMaxSearchWindow       = 32768
	vp8lMaxHashCandidates     = 32
)

type vp8lTokenKind int

const (
	vp8lLiteralToken vp8lTokenKind = iota
	vp8lCopyToken
	vp8lCacheToken
)

type vp8lToken struct {
	kind              vp8lTokenKind
	pixel             uint32
	cacheIndex        int
	lengthPrefix      int
	lengthExtraBits   uint
	lengthExtra       uint32
	distancePrefix    int
	distanceExtraBits uint
	distanceExtra     uint32
}

type huffmanCode struct {
	bits  uint32
	nbits uint
}

type prefixCode struct {
	lengths        []uint8
	codes          []huffmanCode
	usedSymbols    []int
	maxSymbol      int
	alphabetSize   int
	nonZeroLengths int
}

type symbolFrequency struct {
	symbol int
	freq   int
}

var codeLengthCodeOrder = [...]int{
	17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
}

func writeSpatiallyCodedImage(bw *bitWriter, pixels []uint32, width, height int, colorCacheBits int) {
	writeColorCacheInfo(bw, colorCacheBits)
	bw.writeBits(0, 1)
	writeImageData(bw, pixels, width, height, colorCacheBits)
}

func writeEntropyCodedImage(bw *bitWriter, pixels []uint32, width, height int, colorCacheBits int) {
	writeColorCacheInfo(bw, colorCacheBits)
	writeImageData(bw, pixels, width, height, colorCacheBits)
}

func writeColorCacheInfo(bw *bitWriter, colorCacheBits int) {
	if colorCacheBits > 0 {
		bw.writeBits(1, 1)
		bw.writeBits(uint32(colorCacheBits), 4)
	} else {
		bw.writeBits(0, 1)
	}
}

func writeImageData(bw *bitWriter, pixels []uint32, width, height int, colorCacheBits int) {
	tokens := buildVP8LTokens(pixels, width, height, colorCacheBits)

	greenAlphabetSize := vp8lBaseGreenAlphabetSize
	if colorCacheBits > 0 {
		greenAlphabetSize += 1 << colorCacheBits
	}

	greenFreq := make([]int, greenAlphabetSize)
	redFreq := make([]int, vp8lByteAlphabetSize)
	blueFreq := make([]int, vp8lByteAlphabetSize)
	alphaFreq := make([]int, vp8lByteAlphabetSize)
	distanceFreq := make([]int, vp8lDistanceAlphabetSize)

	for _, t := range tokens {
		switch t.kind {
		case vp8lLiteralToken:
			greenFreq[int(argbG(t.pixel))]++
			redFreq[int(argbR(t.pixel))]++
			blueFreq[int(argbB(t.pixel))]++
			alphaFreq[int(argbA(t.pixel))]++
		case vp8lCopyToken:
			greenFreq[256+t.lengthPrefix]++
			distanceFreq[t.distancePrefix]++
		case vp8lCacheToken:
			greenFreq[vp8lBaseGreenAlphabetSize+t.cacheIndex]++
		}
	}

	greenCode := buildBalancedPrefixCode(greenFreq)
	redCode := buildBalancedPrefixCode(redFreq)
	blueCode := buildBalancedPrefixCode(blueFreq)
	alphaCode := buildBalancedPrefixCode(alphaFreq)
	distanceCode := buildBalancedPrefixCode(distanceFreq)

	writePrefixCode(bw, greenCode)
	writePrefixCode(bw, redCode)
	writePrefixCode(bw, blueCode)
	writePrefixCode(bw, alphaCode)
	writePrefixCode(bw, distanceCode)

	for _, t := range tokens {
		switch t.kind {
		case vp8lLiteralToken:
			writeHuffmanCode(bw, greenCode.codes[int(argbG(t.pixel))])
			writeHuffmanCode(bw, redCode.codes[int(argbR(t.pixel))])
			writeHuffmanCode(bw, blueCode.codes[int(argbB(t.pixel))])
			writeHuffmanCode(bw, alphaCode.codes[int(argbA(t.pixel))])
		case vp8lCopyToken:
			writeHuffmanCode(bw, greenCode.codes[256+t.lengthPrefix])
			bw.writeBits(t.lengthExtra, t.lengthExtraBits)
			writeHuffmanCode(bw, distanceCode.codes[t.distancePrefix])
			bw.writeBits(t.distanceExtra, t.distanceExtraBits)
		case vp8lCacheToken:
			writeHuffmanCode(bw, greenCode.codes[vp8lBaseGreenAlphabetSize+t.cacheIndex])
		}
	}
}

func writeVP8LHeader(bw *bitWriter, rgba []byte, width, height int) {
	bw.writeByteAligned(0x2f)
	bw.writeBits(uint32(width-1), 14)
	bw.writeBits(uint32(height-1), 14)

	alphaUsed := false
	for i := 3; i < len(rgba); i += 4 {
		if rgba[i] != 255 {
			alphaUsed = true
			break
		}
	}
	if alphaUsed {
		bw.writeBits(1, 1)
	} else {
		bw.writeBits(0, 1)
	}
	bw.writeBits(0, 3)
}

func buildVP8LTokens(pixels []uint32, width, height int, colorCacheBits int) []vp8lToken {
	pixelCount := len(pixels)
	tokens := make([]vp8lToken, 0, pixelCount)

	var cache []uint32
	if colorCacheBits > 0 {
		cache = make([]uint32, 1<<colorCacheBits)
	}

	chains := make(map[uint64][]int, minInt(pixelCount, 1<<16))
	addToChains := func(pos int) {
		if pos+vp8lLZ77MinLength > pixelCount {
			return
		}
		key := pixelSequenceKey(pixels, pos)
		list := chains[key]
		list = append(list, pos)
		if len(list) > vp8lMaxHashCandidates {
			list = append([]int(nil), list[len(list)-vp8lMaxHashCandidates:]...)
		}
		chains[key] = list
	}

	insertCachePixel := func(p uint32) {
		if colorCacheBits == 0 {
			return
		}
		cache[colorCacheIndex(p, colorCacheBits)] = p
	}

	for pos := 0; pos < pixelCount; {
		bestLen := 0
		bestDistance := 0

		if pos+vp8lLZ77MinLength <= pixelCount {
			key := pixelSequenceKey(pixels, pos)
			candidates := chains[key]
			for i := len(candidates) - 1; i >= 0; i-- {
				cand := candidates[i]
				distance := pos - cand
				if distance <= 0 || distance > vp8lMaxSearchWindow {
					continue
				}
				length := matchLength(pixels, cand, pos, minInt(vp8lMaxBackwardLength, pixelCount-pos))
				if length > bestLen {
					bestLen = length
					bestDistance = distance
					if bestLen == vp8lMaxBackwardLength {
						break
					}
				}
			}
		}

		if bestLen >= vp8lLZ77MinLength {
			lengthPrefix, lengthExtraBits, lengthExtra := prefixCodeForVP8LValue(bestLen)
			distanceCode := scanDistanceToDistanceCode(bestDistance)
			distancePrefix, distanceExtraBits, distanceExtra := prefixCodeForVP8LValue(distanceCode)
			tokens = append(tokens, vp8lToken{kind: vp8lCopyToken, lengthPrefix: lengthPrefix, lengthExtraBits: lengthExtraBits, lengthExtra: lengthExtra, distancePrefix: distancePrefix, distanceExtraBits: distanceExtraBits, distanceExtra: distanceExtra})
			for k := 0; k < bestLen; k++ {
				addToChains(pos + k)
				insertCachePixel(pixels[pos+k])
			}
			pos += bestLen
			continue
		}

		p := pixels[pos]
		if colorCacheBits > 0 {
			idx := colorCacheIndex(p, colorCacheBits)
			if cache[idx] == p {
				tokens = append(tokens, vp8lToken{kind: vp8lCacheToken, cacheIndex: idx})
				insertCachePixel(p)
				addToChains(pos)
				pos++
				continue
			}
		}

		tokens = append(tokens, vp8lToken{kind: vp8lLiteralToken, pixel: p})
		insertCachePixel(p)
		addToChains(pos)
		pos++
	}

	_ = width
	_ = height
	return tokens
}

func pixelSequenceKey(pixels []uint32, pos int) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < vp8lLZ77MinLength; i++ {
		h ^= uint64(pixels[pos+i])
		h *= 1099511628211
	}
	return h
}

func matchLength(pixels []uint32, cand, pos, maxLen int) int {
	length := 0
	for length < maxLen && pixels[cand+length] == pixels[pos+length] {
		length++
	}
	return length
}

func scanDistanceToDistanceCode(scanDistance int) int {
	if scanDistance == 1 {
		return 2
	}
	return scanDistance + 120
}

func prefixCodeForVP8LValue(value int) (prefix int, extraBits uint, extra uint32) {
	if value <= 0 {
		panic("VP8L prefix-coded values are one-based")
	}
	if value <= 4 {
		return value - 1, 0, 0
	}
	for prefix = 4; prefix < 40; prefix++ {
		extraBits = uint((prefix - 2) >> 1)
		offset := (2 + (prefix & 1)) << extraBits
		first := offset + 1
		last := offset + (1 << extraBits)
		if value >= first && value <= last {
			return prefix, extraBits, uint32(value - first)
		}
	}
	panic("VP8L prefix-coded value is too large")
}

func buildBalancedPrefixCode(freq []int) prefixCode {
	used := make([]symbolFrequency, 0, len(freq))
	for symbol, count := range freq {
		if count > 0 {
			used = append(used, symbolFrequency{symbol: symbol, freq: count})
		}
	}
	if len(used) == 0 {
		used = append(used, symbolFrequency{symbol: 0, freq: 1})
	}
	sort.SliceStable(used, func(i, j int) bool {
		if used[i].freq != used[j].freq {
			return used[i].freq > used[j].freq
		}
		return used[i].symbol < used[j].symbol
	})

	lengths := make([]uint8, len(freq))
	if len(used) == 1 {
		lengths[used[0].symbol] = 1
	} else {
		bits := ceilLog2(len(used))
		shortCount := (1 << bits) - len(used)
		for rank, sf := range used {
			if rank < shortCount && bits > 1 {
				lengths[sf.symbol] = uint8(bits - 1)
			} else {
				lengths[sf.symbol] = uint8(bits)
			}
		}
	}

	maxSymbol := 2
	usedSymbols := make([]int, 0, len(used))
	for _, sf := range used {
		usedSymbols = append(usedSymbols, sf.symbol)
		if sf.symbol+1 > maxSymbol {
			maxSymbol = sf.symbol + 1
		}
	}
	codes := makeCanonicalCodes(lengths)
	return prefixCode{lengths: lengths, codes: codes, usedSymbols: usedSymbols, maxSymbol: maxSymbol, alphabetSize: len(freq), nonZeroLengths: len(used)}
}

func ceilLog2(n int) int {
	if n <= 1 {
		return 0
	}
	bits := 0
	value := 1
	for value < n {
		value <<= 1
		bits++
	}
	return bits
}

func makeCanonicalCodes(lengths []uint8) []huffmanCode {
	codes := make([]huffmanCode, len(lengths))
	maxLen := 0
	nonZero := 0
	for _, length := range lengths {
		if length > 0 {
			nonZero++
			if int(length) > maxLen {
				maxLen = int(length)
			}
		}
	}
	if nonZero == 0 {
		return codes
	}
	if nonZero == 1 {
		for symbol, length := range lengths {
			if length > 0 {
				codes[symbol] = huffmanCode{bits: 0, nbits: 0}
				return codes
			}
		}
	}

	blCount := make([]int, maxLen+1)
	for _, length := range lengths {
		if length > 0 {
			blCount[int(length)]++
		}
	}
	nextCode := make([]int, maxLen+1)
	code := 0
	for bits := 1; bits <= maxLen; bits++ {
		code = (code + blCount[bits-1]) << 1
		nextCode[bits] = code
	}
	for symbol, length := range lengths {
		if length == 0 {
			continue
		}
		l := int(length)
		canonicalCode := nextCode[l]
		nextCode[l]++
		codes[symbol] = huffmanCode{bits: reverseBits(uint32(canonicalCode), uint(l)), nbits: uint(l)}
	}
	return codes
}

func writePrefixCode(bw *bitWriter, pc prefixCode) {
	if canUseSimplePrefixCode(pc) {
		writeSimplePrefixCode(bw, pc.usedSymbols)
		return
	}
	writeNormalPrefixCode(bw, pc)
}

func canUseSimplePrefixCode(pc prefixCode) bool {
	if pc.nonZeroLengths < 1 || pc.nonZeroLengths > 2 {
		return false
	}
	for _, symbol := range pc.usedSymbols {
		if symbol > 255 {
			return false
		}
	}
	return true
}

func writeSimplePrefixCode(bw *bitWriter, symbols []int) {
	bw.writeBits(1, 1)
	if len(symbols) == 1 {
		bw.writeBits(0, 1)
		writeSimplePrefixSymbol(bw, symbols[0])
		return
	}
	bw.writeBits(1, 1)
	first := symbols[0]
	second := symbols[1]
	if second <= 1 && first > 1 {
		first, second = second, first
	}
	writeSimplePrefixSymbol(bw, first)
	bw.writeBits(uint32(second), 8)
}

func writeSimplePrefixSymbol(bw *bitWriter, symbol int) {
	if symbol <= 1 {
		bw.writeBits(0, 1)
		bw.writeBits(uint32(symbol), 1)
		return
	}
	bw.writeBits(1, 1)
	bw.writeBits(uint32(symbol), 8)
}

func writeNormalPrefixCode(bw *bitWriter, pc prefixCode) {
	bw.writeBits(0, 1)
	codeLengthFreq := make([]int, 19)
	for i := 0; i < pc.maxSymbol; i++ {
		codeLengthFreq[int(pc.lengths[i])]++
	}
	codeLengthCode := buildBalancedPrefixCode(codeLengthFreq)

	lastCodeLengthIndex := 3
	for i, symbol := range codeLengthCodeOrder {
		if codeLengthCode.lengths[symbol] != 0 {
			lastCodeLengthIndex = i
		}
	}
	numCodeLengths := lastCodeLengthIndex + 1
	if numCodeLengths < 4 {
		numCodeLengths = 4
	}
	bw.writeBits(uint32(numCodeLengths-4), 4)
	for i := 0; i < numCodeLengths; i++ {
		symbol := codeLengthCodeOrder[i]
		bw.writeBits(uint32(codeLengthCode.lengths[symbol]), 3)
	}
	bw.writeBits(1, 1)
	writeMaxSymbol(bw, pc.maxSymbol)
	for i := 0; i < pc.maxSymbol; i++ {
		lengthSymbol := int(pc.lengths[i])
		writeHuffmanCode(bw, codeLengthCode.codes[lengthSymbol])
	}
}

func writeMaxSymbol(bw *bitWriter, maxSymbol int) {
	value := maxSymbol - 2
	if value < 0 {
		value = 0
	}
	neededBits := bitsNeeded(value)
	lengthBits := 2
	selector := 0
	for lengthBits < neededBits {
		lengthBits += 2
		selector++
	}
	bw.writeBits(uint32(selector), 3)
	bw.writeBits(uint32(value), uint(lengthBits))
}

func bitsNeeded(value int) int {
	bits := 1
	for (1 << bits) <= value {
		bits++
	}
	if bits < 2 {
		return 2
	}
	return bits
}

func writeHuffmanCode(bw *bitWriter, code huffmanCode) { bw.writeBits(code.bits, code.nbits) }

func reverseBits(v uint32, n uint) uint32 {
	var out uint32
	for i := uint(0); i < n; i++ {
		out = (out << 1) | (v & 1)
		v >>= 1
	}
	return out
}

func writeAllByteSymbolsPrefixCode(bw *bitWriter) {
	bw.writeBits(0, 1)
	bw.writeBits(8, 4)
	lengths := [12]uint32{0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	for _, l := range lengths {
		bw.writeBits(l, 3)
	}
	bw.writeBits(1, 1)
	bw.writeBits(3, 3)
	bw.writeBits(254, 8)
	for i := 0; i < 256; i++ {
		bw.writeBits(1, 1)
	}
}

func writeSingleSymbolPrefixCode(bw *bitWriter, symbol byte) {
	bw.writeBits(1, 1)
	bw.writeBits(0, 1)
	if symbol <= 1 {
		bw.writeBits(0, 1)
		bw.writeBits(uint32(symbol), 1)
	} else {
		bw.writeBits(1, 1)
		bw.writeBits(uint32(symbol), 8)
	}
}

func writeFixed8Symbol(bw *bitWriter, symbol byte) { bw.writeBits(uint32(reverse8(symbol)), 8) }

func reverse8(v byte) byte {
	v = (v&0xf0)>>4 | (v&0x0f)<<4
	v = (v&0xcc)>>2 | (v&0x33)<<2
	v = (v&0xaa)>>1 | (v&0x55)<<1
	return v
}

type bitWriter struct {
	buf   []byte
	acc   uint64
	nbits uint
}

func (w *bitWriter) writeBits(value uint32, n uint) {
	if n == 0 {
		return
	}
	if n >= 32 {
		panic("bitWriter.writeBits supports at most 31 bits at once")
	}
	mask := uint64(1<<n) - 1
	w.acc |= (uint64(value) & mask) << w.nbits
	w.nbits += n
	for w.nbits >= 8 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc >>= 8
		w.nbits -= 8
	}
}

func (w *bitWriter) writeByteAligned(v byte) {
	if w.nbits == 0 {
		w.buf = append(w.buf, v)
		return
	}
	w.writeBits(uint32(v), 8)
}

func (w *bitWriter) bytes() []byte {
	out := make([]byte, len(w.buf))
	copy(out, w.buf)
	if w.nbits > 0 {
		out = append(out, byte(w.acc))
	}
	return out
}

func rgbaToARGB(rgba []byte) []uint32 {
	pixels := make([]uint32, len(rgba)/4)
	for i, p := 0, 0; i < len(rgba); i, p = i+4, p+1 {
		pixels[p] = packARGB(rgba[i+3], rgba[i+0], rgba[i+1], rgba[i+2])
	}
	return pixels
}

func packARGB(a, r, g, b byte) uint32 {
	return uint32(a)<<24 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}
func argbA(p uint32) byte { return byte(p >> 24) }
func argbR(p uint32) byte { return byte(p >> 16) }
func argbG(p uint32) byte { return byte(p >> 8) }
func argbB(p uint32) byte { return byte(p) }

func subARGB(a, b uint32) uint32 {
	return packARGB(byte(argbA(a)-argbA(b)), byte(argbR(a)-argbR(b)), byte(argbG(a)-argbG(b)), byte(argbB(a)-argbB(b)))
}

func applySubtractGreenTransform(pixels []uint32) {
	for i, p := range pixels {
		g := argbG(p)
		pixels[i] = packARGB(argbA(p), argbR(p)-g, g, argbB(p)-g)
	}
}

func predictorResiduals(pixels []uint32, width, height, mode int) []uint32 {
	out := make([]uint32, len(pixels))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x
			pred := predictorForPixel(pixels, width, height, x, y, mode)
			out[idx] = subARGB(pixels[idx], pred)
		}
	}
	return out
}

func predictorForPixel(pixels []uint32, width, height, x, y, mode int) uint32 {
	_ = height
	if x == 0 && y == 0 {
		return packARGB(255, 0, 0, 0)
	}
	if y == 0 {
		return pixels[y*width+x-1]
	}
	if x == 0 {
		return pixels[(y-1)*width+x]
	}
	l := pixels[y*width+x-1]
	t := pixels[(y-1)*width+x]
	tl := pixels[(y-1)*width+x-1]
	var tr uint32
	if x == width-1 {
		tr = pixels[y*width]
	} else {
		tr = pixels[(y-1)*width+x+1]
	}
	switch mode {
	case 0:
		return packARGB(255, 0, 0, 0)
	case 1:
		return l
	case 2:
		return t
	case 3:
		return tr
	case 4:
		return tl
	case 5:
		return avgARGB(avgARGB(l, tr), t)
	case 6:
		return avgARGB(l, tl)
	case 7:
		return avgARGB(l, t)
	case 8:
		return avgARGB(tl, t)
	case 9:
		return avgARGB(t, tr)
	case 10:
		return avgARGB(avgARGB(l, tl), avgARGB(t, tr))
	case 11:
		return selectPredictor(l, t, tl)
	case 12:
		return clampAddSubtractFullARGB(l, t, tl)
	case 13:
		return clampAddSubtractHalfARGB(avgARGB(l, t), tl)
	default:
		return l
	}
}

func avgARGB(a, b uint32) uint32 {
	return packARGB(byte((int(argbA(a))+int(argbA(b)))/2), byte((int(argbR(a))+int(argbR(b)))/2), byte((int(argbG(a))+int(argbG(b)))/2), byte((int(argbB(a))+int(argbB(b)))/2))
}

func selectPredictor(l, t, tl uint32) uint32 {
	pa := int(argbA(l)) + int(argbA(t)) - int(argbA(tl))
	pr := int(argbR(l)) + int(argbR(t)) - int(argbR(tl))
	pg := int(argbG(l)) + int(argbG(t)) - int(argbG(tl))
	pb := int(argbB(l)) + int(argbB(t)) - int(argbB(tl))
	pl := absInt(pa-int(argbA(l))) + absInt(pr-int(argbR(l))) + absInt(pg-int(argbG(l))) + absInt(pb-int(argbB(l)))
	pt := absInt(pa-int(argbA(t))) + absInt(pr-int(argbR(t))) + absInt(pg-int(argbG(t))) + absInt(pb-int(argbB(t)))
	if pl < pt {
		return l
	}
	return t
}

func clampAddSubtractFullARGB(a, b, c uint32) uint32 {
	return packARGB(byte(clampInt(int(argbA(a))+int(argbA(b))-int(argbA(c)))), byte(clampInt(int(argbR(a))+int(argbR(b))-int(argbR(c)))), byte(clampInt(int(argbG(a))+int(argbG(b))-int(argbG(c)))), byte(clampInt(int(argbB(a))+int(argbB(b))-int(argbB(c)))))
}
func clampAddSubtractHalfARGB(a, b uint32) uint32 {
	return packARGB(byte(clampInt(int(argbA(a))+(int(argbA(a))-int(argbA(b)))/2)), byte(clampInt(int(argbR(a))+(int(argbR(a))-int(argbR(b)))/2)), byte(clampInt(int(argbG(a))+(int(argbG(a))-int(argbG(b)))/2)), byte(clampInt(int(argbB(a))+(int(argbB(a))-int(argbB(b)))/2)))
}

func colorCacheIndex(p uint32, bits int) int {
	return int((uint32(uint64(0x1e35a7bd) * uint64(p))) >> (32 - uint(bits)))
}
func divRoundUp(num, den int) int { return (num + den - 1) / den }
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
func clampInt(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
