// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// PDF object graph resolution: indirect references and object streams.

package pdf

import (
	"fmt"
	"io"
)

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
