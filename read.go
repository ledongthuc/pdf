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

