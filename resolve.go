// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// PDF object graph resolution: indirect references and object streams.

package pdf

import (
	"fmt"
	"io"
)

// objStmHeader validates that strm is a well-formed ObjStm and returns
// the entry count n and the byte offset of the first object body (first).
func objStmHeader(strm Value) (n int, first int64) {
	if strm.Kind() != Stream {
		panic("not a stream")
	}
	if strm.Key("Type").Name() != "ObjStm" {
		panic("not an object stream")
	}
	n = int(strm.Key("N").Int64())
	first = strm.Key("First").Int64()
	if first == 0 {
		panic("missing First")
	}
	return n, first
}

// scanObjStmIndex reads the (id, offset) index pairs from b, and if one
// matches ptr.id it seeks to first+offset and returns the decoded object.
// ok is false when the id is not found in this stream's index.
func scanObjStmIndex(b *buffer, n int, first int64, ptr objptr) (obj object, ok bool) {
	for i := 0; i < n; i++ {
		id, _ := b.readToken().(int64)
		off, _ := b.readToken().(int64)
		if uint32(id) == ptr.id {
			b.seekForward(first + off)
			return b.readObject(), true
		}
	}
	return nil, false
}

// resolveInStream locates ptr inside an ObjStm (object stream) by scanning
// the index entries, following Extends chains as needed.
func (r *Reader) resolveInStream(parent objptr, ptr objptr, xr xref) interface{} {
	strm := r.resolve(parent, xr.stream)
	for {
		n, first := objStmHeader(strm)
		b := newBuffer(strm.Reader(), 0)
		b.allowEOF = true
		if obj, ok := scanObjStmIndex(b, n, first, ptr); ok {
			return obj
		}
		ext := strm.Key("Extends")
		if ext.Kind() != Stream {
			panic("cannot find object in stream")
		}
		strm = ext
	}
}

// loadDirectObject reads the object at xr.offset from the cross-reference
// table, validates the objdef header, and returns the payload object.
func (r *Reader) loadDirectObject(ptr objptr, xr xref) object {
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
	return def.obj
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
			x = r.loadDirectObject(ptr, xr)
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
