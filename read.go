// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package pdf implements reading of PDF files.
//
// # Overview
//
// PDF is Adobe's Portable Document Format, ubiquitous on the internet.
// A PDF document is a complex data format built on a fairly simple structure.
// This package exposes the simple structure along with some wrappers to
// extract basic information. If more complex information is needed, it is
// possible to extract that information by interpreting the structure exposed
// by this package.
//
// Specifically, a PDF is a data structure built from Values, each of which has
// one of the following Kinds:
//
//	Null, for the null object.
//	Integer, for an integer.
//	Real, for a floating-point number.
//	Bool, for a boolean value.
//	Name, for a name constant (as in /Helvetica).
//	String, for a string constant.
//	Dict, for a dictionary of name-value pairs.
//	Array, for an array of values.
//	Stream, for an opaque data stream and associated header dictionary.
//
// The accessors on Value—Int64, Float64, Bool, Name, and so on—return
// a view of the data as the given type. When there is no appropriate view,
// the accessor returns a zero result. For example, the Name accessor returns
// the empty string if called on a Value v for which v.Kind() != Name.
// Returning zero values this way, especially from the Dict and Array accessors,
// which themselves return Values, makes it possible to traverse a PDF quickly
// without writing any error checking. On the other hand, it means that mistakes
// can go unreported.
//
// The basic structure of the PDF file is exposed as the graph of Values.
//
// Most richer data structures in a PDF file are dictionaries with specific interpretations
// of the name-value pairs. The Font and Page wrappers make the interpretation
// of a specific Value as the corresponding type easier. They are only helpers, though:
// they are implemented only in terms of the Value API and could be moved outside
// the package. Equally important, traversal of other PDF data structures can be implemented
// in other packages as needed.
package pdf

// BUG(rsc): The package is incomplete, although it has been used successfully on some
// large real-world PDF files.

// BUG(rsc): There is no support for closing open PDF files. If you drop all references to a Reader,
// the underlying reader will eventually be garbage collected.

// BUG(rsc): The library makes no attempt at efficiency. A value cache maintained in the Reader
// would probably help significantly.

// BUG(rsc): The support for reading encrypted files is weak.

// BUG(rsc): The Value API does not support error reporting. The intent is to allow users to
// set an error reporting callback in Reader, but that code has not been implemented.

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
)

// DebugOn is responsible for logging messages into stdout. If problems arise during reading, set it true.
var DebugOn = false

// A Reader is a single PDF file open for reading.
type Reader struct {
	f          io.ReaderAt
	end        int64
	xref       []xref
	trailer    dict
	trailerptr objptr
	key        []byte
	useAES     bool
}

type xref struct {
	ptr      objptr
	inStream bool
	stream   objptr
	offset   int64
}

func (r *Reader) errorf(format string, args ...interface{}) {
	panic(fmt.Errorf(format, args...))
}

// Open opens a file for reading.
func Open(file string) (*os.File, *Reader, error) {
	f, err := os.Open(file)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	reader, err := NewReader(f, fi.Size())
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, reader, err
}

// NewReader opens a file for reading, using the data in f with the given total size.
func NewReader(f io.ReaderAt, size int64) (*Reader, error) {
	return NewReaderEncrypted(f, size, nil)
}

// OpenBytes opens a PDF from an in-memory byte slice.
func OpenBytes(src []byte) (*Reader, error) {
	return NewReader(bytes.NewReader(src), int64(len(src)))
}

// NewReaderEncrypted opens a file for reading, using the data in f with the given total size.
// If the PDF is encrypted, NewReaderEncrypted calls pw repeatedly to obtain passwords
// to try. If pw returns the empty string, NewReaderEncrypted stops trying to decrypt
// the file and returns an error.
func NewReaderEncrypted(f io.ReaderAt, size int64, pw func() string) (r *Reader, err error) {
	defer func() {
		if x := recover(); x != nil {
			r = nil
			if e, ok := x.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("malformed PDF: %v", x)
			}
		}
	}()

	if err = validatePDFHeader(f); err != nil {
		return nil, err
	}
	startxrefPos, err := findStartxrefOffset(f, size)
	if err != nil {
		return nil, err
	}
	r = &Reader{f: f, end: size}
	b := newBuffer(io.NewSectionReader(f, startxrefPos, size-startxrefPos), startxrefPos)
	if b.readToken() != keyword("startxref") {
		return nil, fmt.Errorf("malformed PDF file: missing startxref")
	}
	startxref, ok := b.readToken().(int64)
	if !ok {
		return nil, fmt.Errorf("malformed PDF file: startxref not followed by integer")
	}
	b = newBuffer(io.NewSectionReader(r.f, startxref, r.end-startxref), startxref)
	r.xref, r.trailerptr, r.trailer, err = readXref(r, b)
	if err != nil {
		return nil, err
	}
	if err = tryDecrypt(r, pw); err != nil {
		return nil, err
	}
	return r, nil
}

// Trailer returns the file's Trailer value.
func (r *Reader) Trailer() Value {
	return Value{r, r.trailerptr, r.trailer}
}

func readXref(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	tok := b.readToken()
	if tok == keyword("xref") {
		return readXrefTable(r, b)
	}
	if _, ok := tok.(int64); ok {
		b.unreadToken(tok)
		return readXrefStream(r, b)
	}
	return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", tok)
}

func readXrefStream(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	obj1 := b.readObject()
	obj, ok := obj1.(objdef)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", objfmt(obj1))
	}
	strmptr := obj.ptr
	strm, ok := obj.obj.(stream)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", objfmt(obj))
	}
	if strm.hdr["Type"] != name("XRef") {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref stream does not have type XRef")
	}
	size, ok := strm.hdr["Size"].(int64)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref stream missing Size")
	}
	table := make([]xref, size)

	table, err := readXrefStreamData(r, strm, table, size)
	if err != nil {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %v", err)
	}

	for prevoff := strm.hdr["Prev"]; prevoff != nil; {
		off, ok := prevoff.(int64)
		if !ok {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref Prev is not integer: %v", prevoff)
		}
		b := newBuffer(io.NewSectionReader(r.f, off, r.end-off), off)
		obj1 := b.readObject()
		obj, ok := obj1.(objdef)
		if !ok {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref prev stream not found: %v", objfmt(obj1))
		}
		prevstrm, ok := obj.obj.(stream)
		if !ok {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref prev stream not found: %v", objfmt(obj))
		}
		prevoff = prevstrm.hdr["Prev"]
		prev := Value{r, objptr{}, prevstrm}
		if prev.Kind() != Stream {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref prev stream is not stream: %v", prev)
		}
		if prev.Key("Type").Name() != "XRef" {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref prev stream does not have type XRef")
		}
		psize := prev.Key("Size").Int64()
		if psize > size {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref prev stream larger than last stream")
		}
		if table, err = readXrefStreamData(r, prev.data.(stream), table, psize); err != nil {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: reading xref prev stream: %v", err)
		}
	}

	return table, strmptr, strm.hdr, nil
}

// parseWArray validates and converts the PDF xref-stream W array to []int.
func parseWArray(ww array) ([]int, error) {
	var w []int
	for _, x := range ww {
		i, ok := x.(int64)
		if !ok || int64(int(i)) != i {
			return nil, fmt.Errorf("invalid W array %v", objfmt(ww))
		}
		w = append(w, int(i))
	}
	if len(w) < 3 {
		return nil, fmt.Errorf("invalid W array %v", objfmt(ww))
	}
	return w, nil
}

// processXrefEntry reads one entry from an xref stream and updates table.
func processXrefEntry(buf []byte, w []int, start int64, i int, table []xref, data io.ReadCloser) ([]xref, error) {
	if _, err := io.ReadFull(data, buf); err != nil {
		return nil, fmt.Errorf("error reading xref stream: %v", err)
	}
	v1 := decodeInt(buf[0:w[0]])
	if w[0] == 0 {
		v1 = 1
	}
	v2 := decodeInt(buf[w[0] : w[0]+w[1]])
	v3 := decodeInt(buf[w[0]+w[1] : w[0]+w[1]+w[2]])
	x := int(start) + i
	for cap(table) <= x {
		table = append(table[:cap(table)], xref{})
	}
	if table[x].ptr != (objptr{}) {
		return table, nil
	}
	switch v1 {
	case 0:
		table[x] = xref{ptr: objptr{0, 65535}}
	case 1:
		table[x] = xref{ptr: objptr{uint32(x), uint16(v3)}, offset: int64(v2)}
	case 2:
		table[x] = xref{ptr: objptr{uint32(x), 0}, inStream: true, stream: objptr{uint32(v2), 0}, offset: int64(v3)}
	default:
		if DebugOn {
			fmt.Printf("invalid xref stream type %d: %x\n", v1, buf)
		}
	}
	return table, nil
}

func readXrefStreamData(r *Reader, strm stream, table []xref, size int64) ([]xref, error) {
	index, _ := strm.hdr["Index"].(array)
	if index == nil {
		index = array{int64(0), size}
	}
	if len(index)%2 != 0 {
		return nil, fmt.Errorf("invalid Index array %v", objfmt(index))
	}
	ww, ok := strm.hdr["W"].(array)
	if !ok {
		return nil, fmt.Errorf("xref stream missing W array")
	}
	w, err := parseWArray(ww)
	if err != nil {
		return nil, err
	}
	v := Value{r, objptr{}, strm}
	wtotal := 0
	for _, wid := range w {
		wtotal += wid
	}
	buf := make([]byte, wtotal)
	data := v.Reader()
	for len(index) > 0 {
		start, ok1 := index[0].(int64)
		n, ok2 := index[1].(int64)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("malformed Index pair %v %v %T %T", objfmt(index[0]), objfmt(index[1]), index[0], index[1])
		}
		index = index[2:]
		for i := 0; i < int(n); i++ {
			table, err = processXrefEntry(buf, w, start, i, table, data)
			if err != nil {
				return nil, err
			}
		}
	}
	return table, nil
}

func decodeInt(b []byte) int {
	x := 0
	for _, c := range b {
		x = x<<8 | int(c)
	}
	return x
}

func readXrefTable(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	var table []xref

	table, err := readXrefTableData(b, table)
	if err != nil {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %v", err)
	}

	trailer, ok := b.readObject().(dict)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref table not followed by trailer dictionary")
	}

	for prevoff := trailer["Prev"]; prevoff != nil; {
		off, ok := prevoff.(int64)
		if !ok {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref Prev is not integer: %v", prevoff)
		}
		b := newBuffer(io.NewSectionReader(r.f, off, r.end-off), off)
		tok := b.readToken()
		if tok != keyword("xref") {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref Prev does not point to xref")
		}
		table, err = readXrefTableData(b, table)
		if err != nil {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %v", err)
		}

		trailer, ok := b.readObject().(dict)
		if !ok {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref Prev table not followed by trailer dictionary")
		}
		prevoff = trailer["Prev"]
	}

	size, ok := trailer[name("Size")].(int64)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: trailer missing /Size entry")
	}

	if size < int64(len(table)) {
		table = table[:size]
	}

	return table, objptr{}, trailer, nil
}

func readXrefTableData(b *buffer, table []xref) ([]xref, error) {
	for {
		tok := b.readToken()
		if tok == keyword("trailer") {
			break
		}
		start, ok1 := tok.(int64)
		n, ok2 := b.readToken().(int64)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("malformed xref table")
		}
		for i := 0; i < int(n); i++ {
			off, ok1 := b.readToken().(int64)
			gen, ok2 := b.readToken().(int64)
			alloc, ok3 := b.readToken().(keyword)
			if !ok1 || !ok2 || !ok3 || alloc != keyword("f") && alloc != keyword("n") {
				return nil, fmt.Errorf("malformed xref table")
			}
			x := int(start) + i
			for cap(table) <= x {
				table = append(table[:cap(table)], xref{})
			}
			if len(table) <= x {
				table = table[:x+1]
			}
			if alloc == "n" && table[x].offset == 0 {
				table[x] = xref{ptr: objptr{uint32(x), uint16(gen)}, offset: int64(off)}
			}
		}
	}
	return table, nil
}

func findLastLine(buf []byte, s string) int {
	bs := []byte(s)
	max := len(buf)
	for {
		i := bytes.LastIndex(buf[:max], bs)
		if i <= 0 || i+len(bs) >= len(buf) {
			return -1
		}
		if (buf[i-1] == '\n' || buf[i-1] == '\r') && (buf[i+len(bs)] == '\n' || buf[i+len(bs)] == '\r') {
			return i
		}
		max = i
	}
}

// findStartxrefFallback scans f backwards for %%EOF when it is not located
// within the last endChunk bytes, then returns the absolute file offset of
// the preceding "startxref" line.  Called only for non-conformant PDFs that
// append data (e.g. a digital signature) after %%EOF.
func findStartxrefFallback(f io.ReaderAt, size int64) (int64, error) {
	const scanChunk = 4096
	const overlap = 4 // len("%%EOF")-1: handles %%EOF straddling a chunk boundary

	var suffix []byte
	for pos := size; pos > 0; {
		start := pos - scanChunk
		if start < 0 {
			start = 0
		}
		chunkLen := pos - start
		combined := make([]byte, chunkLen+int64(len(suffix)))
		f.ReadAt(combined[:chunkLen], start)
		copy(combined[chunkLen:], suffix)

		if idx := bytes.LastIndex(combined, []byte("%%EOF")); idx >= 0 {
			eofAbs := start + int64(idx)
			ctxStart := eofAbs - 512
			if ctxStart < 0 {
				ctxStart = 0
			}
			ctx := make([]byte, eofAbs-ctxStart)
			f.ReadAt(ctx, ctxStart)
			j := findLastLine(ctx, "startxref")
			if j < 0 {
				return -1, fmt.Errorf("%%EOF found but startxref missing")
			}
			return ctxStart + int64(j), nil
		}

		if chunkLen >= int64(overlap) {
			suffix = make([]byte, overlap)
			copy(suffix, combined[:overlap])
		} else {
			suffix = make([]byte, chunkLen)
			copy(suffix, combined[:chunkLen])
		}
		pos = start
	}
	return -1, fmt.Errorf("%%EOF not found in file")
}

// validatePDFHeader reads the first 10 bytes of f and returns an error when
// the %PDF-n.m header is missing or malformed.
func validatePDFHeader(f io.ReaderAt) error {
	buf := make([]byte, 10)
	f.ReadAt(buf, 0)
	if !bytes.HasPrefix(buf, []byte("%PDF-1.")) || buf[7] < '0' || buf[7] > '7' || buf[8] != '\r' && buf[8] != '\n' && buf[8] != ' ' && buf[8] != '\t' {
		return fmt.Errorf("not a PDF file: invalid header")
	}
	return nil
}

// findStartxrefOffset locates the "startxref" keyword near the end of f and
// returns its absolute byte offset.  It searches the last 1024 bytes first;
// when %%EOF is not found there, it falls back to a full reverse scan.
func findStartxrefOffset(f io.ReaderAt, size int64) (int64, error) {
	const endChunk = 1024
	readStart := size - endChunk
	if readStart < 0 {
		readStart = 0
	}
	buf := make([]byte, size-readStart)
	f.ReadAt(buf, readStart)
	buf = bytes.TrimRight(buf, "\r\n\t ")
	if bytes.HasSuffix(buf, []byte("%%EOF")) {
		i := findLastLine(buf, "startxref")
		if i < 0 {
			return -1, fmt.Errorf("malformed PDF file: missing final startxref")
		}
		return readStart + int64(i), nil
	}
	pos, err := findStartxrefFallback(f, size)
	if err != nil {
		return -1, fmt.Errorf("not a PDF file: missing %%%%EOF")
	}
	return pos, nil
}


func objfmt(x interface{}) string {
	switch x := x.(type) {
	default:
		return fmt.Sprint(x)
	case string:
		if isPDFDocEncoded(x) {
			return strconv.Quote(pdfDocDecode(x))
		}
		if isUTF16(x) {
			return strconv.Quote(utf16Decode(x[2:]))
		}
		return strconv.Quote(x)
	case name:
		return "/" + string(x)
	case dict:
		var keys []string
		for k := range x {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		buf.WriteString("<<")
		for i, k := range keys {
			elem := x[name(k)]
			if i > 0 {
				buf.WriteString(" ")
			}
			buf.WriteString("/")
			buf.WriteString(k)
			buf.WriteString(" ")
			buf.WriteString(objfmt(elem))
		}
		buf.WriteString(">>")
		return buf.String()

	case array:
		var buf bytes.Buffer
		buf.WriteString("[")
		for i, elem := range x {
			if i > 0 {
				buf.WriteString(" ")
			}
			buf.WriteString(objfmt(elem))
		}
		buf.WriteString("]")
		return buf.String()

	case stream:
		return fmt.Sprintf("%v@%d", objfmt(x.hdr), x.offset)

	case objptr:
		return fmt.Sprintf("%d %d R", x.id, x.gen)

	case objdef:
		return fmt.Sprintf("{%d %d obj}%v", x.ptr.id, x.ptr.gen, objfmt(x.obj))
	}
}


// resolveInStream locates ptr inside an ObjStm (object stream) by scanning
// the index entries, following Extends chains as needed.
func (r *Reader) resolveInStream(parent objptr, ptr objptr, xr xref) interface{} {
	strm := r.resolve(parent, xr.stream)
Search:
	for {
		if strm.Kind() != Stream {
			panic("not a stream")
		}
		if strm.Key("Type").Name() != "ObjStm" {
			panic("not an object stream")
		}
		n := int(strm.Key("N").Int64())
		first := strm.Key("First").Int64()
		if first == 0 {
			panic("missing First")
		}
		b := newBuffer(strm.Reader(), 0)
		b.allowEOF = true
		for i := 0; i < n; i++ {
			id, _ := b.readToken().(int64)
			off, _ := b.readToken().(int64)
			if uint32(id) == ptr.id {
				b.seekForward(first + off)
				return b.readObject()
			}
		}
		ext := strm.Key("Extends")
		if ext.Kind() != Stream {
			panic("cannot find object in stream")
		}
		strm = ext
		continue Search
	}
}

func (r *Reader) resolve(parent objptr, x interface{}) Value {
	if ptr, ok := x.(objptr); ok {
		if ptr.id >= uint32(len(r.xref)) {
			return Value{}
		}
		xr := r.xref[ptr.id]
		if xr.ptr != ptr || !xr.inStream && xr.offset == 0 {
			return Value{}
		}
		if xr.inStream {
			x = r.resolveInStream(parent, ptr, xr)
		} else {
			b := newBuffer(io.NewSectionReader(r.f, xr.offset, r.end-xr.offset), xr.offset)
			b.key = r.key
			b.useAES = r.useAES
			obj := b.readObject()
			def, ok := obj.(objdef)
			if !ok {
				panic(fmt.Errorf("loading %v: found %T instead of objdef", ptr, obj))
			}
			if def.ptr != ptr {
				panic(fmt.Errorf("loading %v: found %v", ptr, def.ptr))
			}
			x = def.obj
		}
		parent = ptr
	}

	switch x := x.(type) {
	case nil, bool, int64, float64, name, dict, array, stream:
		return Value{r, parent, x}
	case string:
		return Value{r, parent, x}
	default:
		panic(fmt.Errorf("unexpected value type %T in resolve", x))
	}
}


func applyFilter(rd io.Reader, name string, param Value) io.Reader {
	switch name {
	default:
		panic("unknown filter " + name)
	case "FlateDecode":
		zr, err := zlib.NewReader(rd)
		if err != nil {
			panic(err)
		}
		pred := param.Key("Predictor")
		if pred.Kind() == Null {
			return zr
		}
		columns := param.Key("Columns").Int64()
		switch pred.Int64() {
		default:
			if DebugOn {
				fmt.Println("unknown predictor", pred)
			}
			panic("pred")
		case 12:
			return &pngUpReader{r: zr, hist: make([]byte, 1+columns), tmp: make([]byte, 1+columns)}
		}
	case "ASCII85Decode":
		cleanASCII85 := newAlphaReader(rd)
		decoder := ascii85.NewDecoder(cleanASCII85)

		switch param.Keys() {
		default:
			if DebugOn {
				fmt.Println("param=", param)
			}
			panic("not expected DecodeParms for ascii85")
		case nil:
			return decoder
		}
	}
}

type pngUpReader struct {
	r    io.Reader
	hist []byte
	tmp  []byte
	pend []byte
}

func (r *pngUpReader) Read(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		if len(r.pend) > 0 {
			m := copy(b, r.pend)
			n += m
			b = b[m:]
			r.pend = r.pend[m:]
			continue
		}
		_, err := io.ReadFull(r.r, r.tmp)
		if err != nil {
			return n, err
		}
		if r.tmp[0] != 2 {
			return n, fmt.Errorf("malformed PNG-Up encoding")
		}
		for i, b := range r.tmp {
			r.hist[i] += b
		}
		r.pend = r.hist[1:]
	}
	return n, nil
}

