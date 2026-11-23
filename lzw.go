// Copyright 2026 The Go Authors and contributors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"sync/atomic"
)

// ErrStreamLimit indicates that decoding a PDF stream exceeded a Reader's
// configured per-stream or cumulative byte budget.
var ErrStreamLimit = errors.New("pdf: decoded stream exceeds byte limit")

// ReaderOptions configures optional password callback and decoded-stream byte
// budgets for NewReaderWithOptions. Zero or negative byte limits leave that
// cap disabled, preserving backward compatibility for NewReader callers.
// MaxStreamBytes applies separately to every filter and non-identity predictor
// output (or to raw bytes for an unfiltered stream). MaxTotalReadBytes counts all
// such stages, including repeated reads, cumulatively across the Reader. It also
// independently bounds populated XRef entries at 32 budget bytes per entry.
// These are decoded-work limits, not bounds on process RSS: fixed decoder buffers
// and predictor rows (at most 1 MiB each) are allocated separately.
type ReaderOptions struct {
	Password          func() string
	MaxStreamBytes    int64
	MaxTotalReadBytes int64
}

const (
	lzwClearCode         = 256
	lzwEODCode           = 257
	lzwFirstCode         = 258
	lzwMaxWidth          = 12
	lzwTableSize         = 1 << lzwMaxWidth // 4096
	lzwInvalidCode       = 0xffff
	lzwFlushBuffer       = 1 << lzwMaxWidth // 4096
	maxFilterChain       = 4
	maxPredictorColors   = 32
	maxPredictorRowBytes = 1 << 20 // 1 MiB max scanline state
	maxDenseXrefEntries  = 4096    // IDs >= 4096 go into Reader.xrefSparse
	maxXrefTableEntries  = 1 << 20 // global ceiling on populated xref entries
	xrefEntryBudgetBytes = 32      // sizeof(xref) charged against MaxTotalReadBytes
)

var errLZWClosed = errors.New("pdf/lzw: reader is closed")

// pdfLZWDecoder implements PDF 32000-1:2008 section 7.4.4.2 LZWDecode (MSB
// bit order, 8-bit literals, 9..12-bit codes, Clear=256, EOD=257, EarlyChange
// in {0, 1} with PDF default 1).
type pdfLZWDecoder struct {
	r           io.ByteReader
	earlyChange uint16 // 0 or 1
	bits        uint32
	nBits       uint
	width       uint
	err         error
	nextCode    uint16 // next free table slot in [258, 4096]
	prevCode    uint16 // previous code or lzwInvalidCode
	suffix      [lzwTableSize]uint8
	prefix      [lzwTableSize]uint16
	output      [2 * lzwFlushBuffer]byte
	o           int
	toRead      []byte
}

func newLZWReader(src io.Reader, earlyChange int) (io.ReadCloser, error) {
	if earlyChange != 0 && earlyChange != 1 {
		return nil, fmt.Errorf("malformed PDF: invalid LZW EarlyChange %d", earlyChange)
	}
	br, ok := src.(io.ByteReader)
	if !ok {
		br = bufio.NewReader(src)
	}
	return &pdfLZWDecoder{
		r:           br,
		earlyChange: uint16(earlyChange),
		width:       9,
		nextCode:    lzwFirstCode,
		prevCode:    lzwInvalidCode,
	}, nil
}

func (d *pdfLZWDecoder) readCode() (uint16, error) {
	for d.nBits < d.width {
		b, err := d.r.ReadByte()
		if err != nil {
			return 0, err
		}
		d.bits |= uint32(b) << (24 - d.nBits)
		d.nBits += 8
	}
	code := uint16(d.bits >> (32 - d.width))
	d.bits <<= d.width
	d.nBits -= d.width
	return code, nil
}

func (d *pdfLZWDecoder) Read(p []byte) (int, error) {
	if len(p) == 0 {
		if d.err == errLZWClosed {
			return 0, d.err
		}
		return 0, nil
	}
	for {
		if len(d.toRead) > 0 {
			n := copy(p, d.toRead)
			d.toRead = d.toRead[n:]
			return n, nil
		}
		if d.err != nil {
			return 0, d.err
		}
		d.decode()
	}
}

func (d *pdfLZWDecoder) Close() error {
	d.err = errLZWClosed
	d.toRead = nil
	return nil
}

func (d *pdfLZWDecoder) decode() {
loop:
	for {
		code, err := d.readCode()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			d.err = err
			break
		}
		switch {
		case code < lzwClearCode:
			d.output[d.o] = uint8(code)
			d.o++
			if d.prevCode != lzwInvalidCode && d.nextCode < lzwTableSize {
				d.suffix[d.nextCode] = uint8(code)
				d.prefix[d.nextCode] = d.prevCode
				d.nextCode++
			}
			d.prevCode = code

		case code == lzwClearCode:
			d.width = 9
			d.nextCode = lzwFirstCode
			d.prevCode = lzwInvalidCode
			continue

		case code == lzwEODCode:
			d.err = io.EOF
			break loop

		case code <= d.nextCode && code < lzwTableSize:
			if code == d.nextCode && d.prevCode == lzwInvalidCode {
				d.err = errors.New("pdf/lzw: invalid code after clear")
				break loop
			}
			c, i := code, len(d.output)-1
			if code == d.nextCode {
				c = d.prevCode
				for c >= lzwFirstCode {
					c = d.prefix[c]
				}
				d.output[i] = uint8(c)
				i--
				c = d.prevCode
			}
			for c >= lzwFirstCode {
				d.output[i] = d.suffix[c]
				i--
				c = d.prefix[c]
			}
			d.output[i] = uint8(c)
			d.o += copy(d.output[d.o:], d.output[i:])
			if d.prevCode != lzwInvalidCode && d.nextCode < lzwTableSize {
				d.suffix[d.nextCode] = uint8(c)
				d.prefix[d.nextCode] = d.prevCode
				d.nextCode++
			}
			d.prevCode = code

		default:
			d.err = errors.New("pdf/lzw: invalid code")
			break loop
		}

		if d.width < lzwMaxWidth && d.nextCode == (uint16(1)<<d.width)-d.earlyChange {
			d.width++
		}
		if d.o >= lzwFlushBuffer {
			break
		}
	}
	d.toRead = d.output[:d.o]
	d.o = 0
}

func applyPredictor(rd io.Reader, param Value) (io.Reader, error) {
	predVal := param.Key("Predictor")
	if predVal.Kind() == Null {
		return rd, nil
	}
	if predVal.Kind() != Integer {
		return nil, fmt.Errorf("malformed PDF: invalid Predictor %v", predVal)
	}
	pred := predVal.Int64()
	if pred == 1 {
		return rd, nil
	}

	colors := int64(1)
	if cv := param.Key("Colors"); cv.Kind() != Null {
		if cv.Kind() != Integer {
			return nil, fmt.Errorf("malformed PDF: invalid predictor Colors %v", cv)
		}
		colors = cv.Int64()
	}
	if colors < 1 || colors > maxPredictorColors {
		return nil, fmt.Errorf("malformed PDF: invalid predictor Colors %d", colors)
	}

	bpc := int64(8)
	if bv := param.Key("BitsPerComponent"); bv.Kind() != Null {
		if bv.Kind() != Integer {
			return nil, fmt.Errorf("malformed PDF: invalid predictor BitsPerComponent %v", bv)
		}
		bpc = bv.Int64()
	}
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		return nil, fmt.Errorf("malformed PDF: invalid predictor BitsPerComponent %d", bpc)
	}

	columns := int64(1)
	if colVal := param.Key("Columns"); colVal.Kind() != Null {
		if colVal.Kind() != Integer {
			return nil, fmt.Errorf("malformed PDF: invalid predictor Columns %v", colVal)
		}
		columns = colVal.Int64()
	}
	bitsPerPixel := colors * bpc
	if columns < 1 || columns > (maxPredictorRowBytes*8)/bitsPerPixel {
		return nil, fmt.Errorf("malformed PDF: invalid predictor Columns %d", columns)
	}
	rowBytes := int((columns*bitsPerPixel + 7) / 8)
	bpp := int((bitsPerPixel + 7) / 8)

	switch {
	case pred == 2:
		return &tiffPredictorReader{
			r:       rd,
			colors:  int(colors),
			bpc:     int(bpc),
			columns: int(columns),
			row:     make([]byte, rowBytes),
		}, nil
	case pred >= 10 && pred <= 15:
		return &pngPredictorReader{
			r:    rd,
			bpp:  bpp,
			prev: make([]byte, rowBytes),
			cur:  make([]byte, rowBytes),
			raw:  make([]byte, 1+rowBytes),
		}, nil
	default:
		return nil, fmt.Errorf("malformed PDF: unsupported Predictor %d", pred)
	}
}

type pngPredictorReader struct {
	r    io.Reader
	bpp  int
	prev []byte
	cur  []byte
	raw  []byte
	pend []byte
	err  error
}

func (r *pngPredictorReader) Read(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		if len(r.pend) > 0 {
			m := copy(p, r.pend)
			n += m
			p = p[m:]
			r.pend = r.pend[m:]
			continue
		}
		if r.err != nil {
			return n, r.err
		}
		nr, err := io.ReadFull(r.r, r.raw)
		if err != nil {
			if err == io.EOF && nr == 0 {
				r.err = io.EOF
				return n, io.EOF
			}
			r.err = err
			return n, err
		}
		tag := r.raw[0]
		src := r.raw[1:]
		switch tag {
		case 0: // None
			copy(r.cur, src)
		case 1: // Sub
			for i, v := range src {
				var left byte
				if i >= r.bpp {
					left = r.cur[i-r.bpp]
				}
				r.cur[i] = v + left
			}
		case 2: // Up
			for i, v := range src {
				r.cur[i] = v + r.prev[i]
			}
		case 3: // Average
			for i, v := range src {
				var left int
				if i >= r.bpp {
					left = int(r.cur[i-r.bpp])
				}
				r.cur[i] = v + byte((left+int(r.prev[i]))>>1)
			}
		case 4: // Paeth
			for i, v := range src {
				var left, upLeft byte
				if i >= r.bpp {
					left = r.cur[i-r.bpp]
					upLeft = r.prev[i-r.bpp]
				}
				r.cur[i] = v + paethPredictor(left, r.prev[i], upLeft)
			}
		default:
			r.err = fmt.Errorf("malformed PDF: invalid PNG predictor filter %d", tag)
			return n, r.err
		}
		copy(r.prev, r.cur)
		r.pend = r.cur
	}
	return n, nil
}

func paethPredictor(a, b, c byte) byte {
	ia, ib, ic := int(a), int(b), int(c)
	p := ia + ib - ic
	pa := absInt(p - ia)
	pb := absInt(p - ib)
	pc := absInt(p - ic)
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

type tiffPredictorReader struct {
	r       io.Reader
	colors  int
	bpc     int
	columns int
	row     []byte
	pend    []byte
	err     error
}

func (r *tiffPredictorReader) Read(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		if len(r.pend) > 0 {
			m := copy(p, r.pend)
			n += m
			p = p[m:]
			r.pend = r.pend[m:]
			continue
		}
		if r.err != nil {
			return n, r.err
		}
		nr, err := io.ReadFull(r.r, r.row)
		if err != nil {
			if err == io.EOF && nr == 0 {
				r.err = io.EOF
				return n, io.EOF
			}
			r.err = err
			return n, err
		}
		switch r.bpc {
		case 8:
			for i := r.colors; i < len(r.row); i++ {
				r.row[i] += r.row[i-r.colors]
			}
		case 16:
			stride := 2 * r.colors
			for i := stride; i+1 < len(r.row); i += 2 {
				prev := uint16(r.row[i-stride])<<8 | uint16(r.row[i-stride+1])
				cur := uint16(r.row[i])<<8 | uint16(r.row[i+1])
				sum := prev + cur
				r.row[i] = byte(sum >> 8)
				r.row[i+1] = byte(sum)
			}
		default: // 1, 2, 4 bits per component, MSB-first
			totalSamples := r.columns * r.colors
			mask := byte((1 << r.bpc) - 1)
			getSample := func(idx int) byte {
				bitPos := idx * r.bpc
				byteIdx := bitPos >> 3
				shift := uint(8 - r.bpc - (bitPos & 7))
				return (r.row[byteIdx] >> shift) & mask
			}
			setSample := func(idx int, val byte) {
				bitPos := idx * r.bpc
				byteIdx := bitPos >> 3
				shift := uint(8 - r.bpc - (bitPos & 7))
				r.row[byteIdx] = (r.row[byteIdx] &^ (mask << shift)) | ((val & mask) << shift)
			}
			for s := r.colors; s < totalSamples; s++ {
				setSample(s, getSample(s)+getSample(s-r.colors))
			}
		}
		r.pend = r.row
	}
	return n, nil
}

// boundedStageReader caps decoded bytes produced by a single stream or filter stage,
// optionally also accounting against a Reader-wide cumulative atomic counter.
type boundedStageReader struct {
	r         io.Reader
	maxStage  int64
	stageRead int64
	totalUsed *atomic.Int64
	maxTotal  int64
}

func wrapStageLimit(rd io.Reader, maxStage int64, totalUsed *atomic.Int64, maxTotal int64) io.Reader {
	if maxTotal <= 0 {
		totalUsed = nil
	}
	if maxStage <= 0 && totalUsed == nil {
		return rd
	}
	return &boundedStageReader{
		r:         rd,
		maxStage:  maxStage,
		totalUsed: totalUsed,
		maxTotal:  maxTotal,
	}
}

func (b *boundedStageReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	limitLen := int64(len(p))
	if b.maxStage > 0 {
		if b.stageRead > b.maxStage {
			return 0, fmt.Errorf("%w (%d bytes)", ErrStreamLimit, b.maxStage)
		}
		rem := b.maxStage - b.stageRead
		if rem < math.MaxInt64 {
			rem++
		}
		if rem < limitLen {
			limitLen = rem
		}
	}
	if b.totalUsed != nil && b.maxTotal > 0 {
		cur := b.totalUsed.Load()
		if cur > b.maxTotal {
			return 0, fmt.Errorf("%w (%d bytes)", ErrStreamLimit, b.maxTotal)
		}
		rem := b.maxTotal - cur
		if rem < math.MaxInt64 {
			rem++
		}
		if rem < limitLen {
			limitLen = rem
		}
	}
	n, err := b.r.Read(p[:limitLen])
	if n > 0 {
		b.stageRead += int64(n)
		var tot int64
		if b.totalUsed != nil {
			tot = b.totalUsed.Add(int64(n))
		}
		if b.maxStage > 0 && b.stageRead > b.maxStage {
			return 0, fmt.Errorf("%w (%d bytes)", ErrStreamLimit, b.maxStage)
		}
		if b.totalUsed != nil && b.maxTotal > 0 && tot > b.maxTotal {
			return 0, fmt.Errorf("%w (%d bytes)", ErrStreamLimit, b.maxTotal)
		}
	}
	return n, err
}

type chainReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (c *chainReadCloser) Close() error {
	var firstErr error
	for i := len(c.closers) - 1; i >= 0; i-- {
		if err := c.closers[i].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	c.closers = nil
	return firstErr
}
