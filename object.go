// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// PDF object type definitions and buffer methods for parsing objects.

package pdf

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
)

const maxObjectDepth = 1000

// An object is a PDF syntax object, one of the following Go types:
//
//	bool, a PDF boolean
//	int64, a PDF integer
//	float64, a PDF real
//	string, a PDF string literal
//	name, a PDF name without the leading slash
//	dict, a PDF dictionary
//	array, a PDF array
//	stream, a PDF stream
//	objptr, a PDF object reference
//	objdef, a PDF object definition
//
// An object may also be nil, to represent the PDF null.
type object interface{}

type dict map[name]object

type array []object

type stream struct {
	hdr    dict
	ptr    objptr
	offset int64
}

type objptr struct {
	id  uint32
	gen uint16
}

type objdef struct {
	ptr objptr
	obj object
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

// readKeywordObject dispatches a keyword token to the appropriate object
// constructor. It is called by readObject when the first token is a keyword.
func (b *buffer) readKeywordObject(kw keyword) object {
	switch kw {
	case "null":
		return nil
	case "<<":
		return b.readDict()
	case "[":
		return b.readArray()
	case ">>", "]":
		// end-of-container sentinel; stop object parsing here
		return nil
	}
	b.errorf("unexpected keyword %q parsing object", kw)
	return nil
}

// tryReadObjRef attempts to read the gen-number and 'R'/'obj' keyword that
// follow an object-id integer t1, completing an indirect reference (objptr)
// or definition (objdef). Returns (result, true) on success. On failure it
// unreads the extra tokens (tok3 then tok2, LIFO) and returns (nil, false)
// so the caller can fall back to returning t1 directly.
func (b *buffer) tryReadObjRef(t1 int64) (object, bool) {
	tok2 := b.readToken()
	t2, ok := tok2.(int64)
	if !ok || int64(uint16(t2)) != t2 {
		b.unreadToken(tok2)
		return nil, false
	}
	tok3 := b.readToken()
	switch tok3 {
	case keyword("R"):
		return objptr{uint32(t1), uint16(t2)}, true
	case keyword("obj"):
		old := b.objptr
		b.objptr = objptr{uint32(t1), uint16(t2)}
		obj := b.readObject()
		if _, ok := obj.(stream); !ok {
			tok4 := b.readToken()
			if tok4 != keyword("endobj") {
				b.errorf("missing endobj after indirect object definition")
				b.unreadToken(tok4)
			}
		}
		b.objptr = old
		return objdef{objptr{uint32(t1), uint16(t2)}, obj}, true
	}
	b.unreadToken(tok3)
	b.unreadToken(tok2)
	return nil, false
}

// maybeDecryptToken decrypts a string token when encryption is active for the
// current object. Non-string tokens and unencrypted contexts are returned
// unchanged.
func (b *buffer) maybeDecryptToken(tok object) object {
	if str, ok := tok.(string); ok && b.key != nil && b.objptr.id != 0 {
		return decryptString(b.key, b.useAES, b.objptr, str)
	}
	return tok
}

// maybeReadObjRef tries to complete an indirect reference or definition when
// tok is a valid object-id integer. Returns (result, true) on success.
func (b *buffer) maybeReadObjRef(tok object) (object, bool) {
	t1, ok := tok.(int64)
	if !ok || int64(uint32(t1)) != t1 {
		return nil, false
	}
	return b.tryReadObjRef(t1)
}

func (b *buffer) readObject() object {
	b.depth++
	defer func() { b.depth-- }()
	if b.depth > maxObjectDepth {
		b.errorf("object nesting exceeds maximum depth %d", maxObjectDepth)
		return nil
	}

	tok := b.readToken()
	if kw, ok := tok.(keyword); ok {
		return b.readKeywordObject(kw)
	}

	tok = b.maybeDecryptToken(tok)

	if !b.allowObjptr {
		return tok
	}

	if result, matched := b.maybeReadObjRef(tok); matched {
		return result
	}
	return tok
}

func (b *buffer) readArray() object {
	var x array
	for {
		tok := b.readToken()
		if tok == nil || tok == io.EOF || tok == keyword("]") {
			break
		}
		b.unreadToken(tok)
		x = append(x, b.readObject())
	}
	return x
}

// consumeStreamNewline consumes the mandatory line ending (CR, CRLF, or LF)
// that the PDF spec requires immediately after the "stream" keyword.
func (b *buffer) consumeStreamNewline() {
	switch b.readByte() {
	case '\r':
		if b.readByte() != '\n' {
			b.unreadByte()
		}
	case '\n':
		// ok
	default:
		b.errorf("stream keyword not followed by newline")
	}
}

// readDictStream checks whether the dict x is followed by a "stream" keyword
// and, if so, returns a stream object. When allowStream is false or no stream
// keyword appears the plain dict is returned unchanged.
func (b *buffer) readDictStream(x dict) object {
	if !b.allowStream {
		return x
	}

	tok := b.readToken()
	if tok != keyword("stream") {
		b.unreadToken(tok)
		return x
	}

	b.consumeStreamNewline()
	return stream{x, b.objptr, b.readOffset()}
}

func (b *buffer) readDict() object {
	x := make(dict)
	for {
		tok := b.readToken()
		if tok == nil || tok == keyword(">>") {
			break
		}
		if tok == io.EOF {
			b.readToken()
			break
		}
		n, ok := tok.(name)
		if !ok {
			fmt.Printf("DEBUG: %T(%v)\n. Skip dict", tok, tok)
			b.errorf("unexpected non-name key %T(%v) parsing dictionary", tok, tok)
			continue
		}
		x[n] = b.readObject()
	}

	return b.readDictStream(x)
}
