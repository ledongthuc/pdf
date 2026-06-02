// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Reading of PDF tokens and objects from a raw byte stream.

package pdf

import (
	"fmt"
	"io"
	"strconv"
)

// A token is a PDF token in the input stream, one of the following Go types:
//
//	bool, a PDF boolean
//	int64, a PDF integer
//	float64, a PDF real
//	string, a PDF string literal
//	keyword, a PDF keyword
//	name, a PDF name without the leading slash
type token interface{}

// A name is a PDF name, without the leading slash.
type name string

// A keyword is a PDF keyword.
// Delimiter tokens used in higher-level syntax,
// such as "<<", ">>", "[", "]", "{", "}", are also treated as keywords.
type keyword string

const maxObjectDepth = 1000

// A buffer holds buffered input bytes from the PDF file.
type buffer struct {
	r           io.Reader // source of data
	buf         []byte    // buffered data
	pos         int       // read index in buf
	offset      int64     // offset at end of buf; aka offset of next read
	tmp         []byte    // scratch space for accumulating token
	unread      []token   // queue of read but then unread tokens
	allowEOF    bool
	allowObjptr bool
	allowStream bool
	eof         bool
	key         []byte
	useAES      bool
	objptr      objptr
	depth       int
}

// newBuffer returns a new buffer reading from r at the given offset.
func newBuffer(r io.Reader, offset int64) *buffer {
	return &buffer{
		r:           r,
		offset:      offset,
		buf:         make([]byte, 0, 4096),
		allowObjptr: true,
		allowStream: true,
	}
}

func (b *buffer) seek(offset int64) {
	b.offset = offset
	b.buf = b.buf[:0]
	b.pos = 0
	b.unread = b.unread[:0]
}

func (b *buffer) readByte() byte {
	if b.pos >= len(b.buf) {
		b.reload()
		if b.pos >= len(b.buf) {
			return '\n'
		}
	}
	c := b.buf[b.pos]
	b.pos++
	return c
}

func (b *buffer) errorf(format string, args ...interface{}) {
	panic(fmt.Errorf(format, args...))
}

func (b *buffer) reload() bool {
	n := cap(b.buf) - int(b.offset%int64(cap(b.buf)))
	n, err := b.r.Read(b.buf[:n])
	if n == 0 && err != nil {
		b.buf = b.buf[:0]
		b.pos = 0
		if b.allowEOF && err == io.EOF {
			b.eof = true
			return false
		}
		b.errorf("malformed PDF: reading at offset %d: %v", b.offset, err)
		return false
	}
	b.offset += int64(n)
	b.buf = b.buf[:n]
	b.pos = 0
	return true
}

func (b *buffer) seekForward(offset int64) {
	for b.offset < offset {
		if !b.reload() {
			return
		}
	}
	b.pos = len(b.buf) - int(b.offset-offset)
}

func (b *buffer) readOffset() int64 {
	return b.offset - int64(len(b.buf)) + int64(b.pos)
}

func (b *buffer) unreadByte() {
	if b.pos > 0 {
		b.pos--
	}
}

func (b *buffer) unreadToken(t token) {
	b.unread = append(b.unread, t)
}

// skipSpaceAndComments advances past whitespace and % comments, returning
// the first non-whitespace byte and a bool that is true if EOF was reached
// during whitespace (the caller should return io.EOF in that case).
func (b *buffer) skipSpaceAndComments() (byte, bool) {
	c := b.readByte()
	for {
		if isSpace(c) {
			if b.eof {
				return 0, true
			}
			c = b.readByte()
		} else if c == '%' {
			for c != '\r' && c != '\n' {
				c = b.readByte()
			}
		} else {
			break
		}
	}
	return c, false
}

// readAngleBracketOpen handles the '<' case: '<<' becomes the dict-open
// keyword; anything else is a hex string.
func (b *buffer) readAngleBracketOpen() token {
	if b.readByte() == '<' {
		return keyword("<<")
	}
	b.unreadByte()
	return b.readHexString()
}

func (b *buffer) readToken() token {
	if n := len(b.unread); n > 0 {
		t := b.unread[n-1]
		b.unread = b.unread[:n-1]
		return t
	}

	c, eof := b.skipSpaceAndComments()
	if eof {
		return io.EOF
	}

	switch c {
	case '<':
		return b.readAngleBracketOpen()

	case '(':
		return b.readLiteralString()

	case '[', ']', '{', '}':
		return keyword(string(c))

	case '/':
		return b.readName()

	case '>':
		if b.readByte() == '>' {
			return keyword(">>")
		}
		// '>' alone is a delimiter; '<<'/'>>'-pairing is the only valid use.
		b.unreadByte()
		b.errorf("unexpected delimiter %#q", rune(c))
		return nil

	default:
		if isDelim(c) {
			b.errorf("unexpected delimiter %#q", rune(c))
			return nil
		}
		b.unreadByte()
		return b.readKeyword()
	}
}

func (b *buffer) readHexString() token {
	tmp := b.tmp[:0]
	for {
	Loop:
		c := b.readByte()
		if b.eof {
			break
		}
		if c == '>' {
			break
		}
		if isSpace(c) {
			goto Loop
		}
	Loop2:
		c2 := b.readByte()
		if b.eof {
			break
		}
		if isSpace(c2) {
			goto Loop2
		}
		x := unhex(c)<<4 | unhex(c2)
		if x < 0 {
			b.errorf("malformed hex string %c %c %s", c, c2, b.buf[b.pos:])
			break
		}
		tmp = append(tmp, byte(x))
	}
	b.tmp = tmp
	return string(tmp)
}

func unhex(b byte) int {
	switch {
	case '0' <= b && b <= '9':
		return int(b) - '0'
	case 'a' <= b && b <= 'f':
		return int(b) - 'a' + 10
	case 'A' <= b && b <= 'F':
		return int(b) - 'A' + 10
	}
	return -1
}

// appendEscape decodes the backslash-escape sequence whose character after
// the backslash is c, appends the decoded bytes to tmp, and returns the
// grown slice. The leading backslash has already been consumed by the caller.
func (b *buffer) appendEscape(tmp []byte, c byte) []byte {
	switch c {
	default:
		b.errorf("invalid escape sequence \\%c", c)
		return append(tmp, '\\', c)
	case 'n':
		return append(tmp, '\n')
	case 'r':
		return append(tmp, '\r')
	case 'b':
		return append(tmp, '\b')
	case 't':
		return append(tmp, '\t')
	case 'f':
		return append(tmp, '\f')
	case '(', ')', '\\':
		return append(tmp, c)
	case '\r':
		if b.readByte() != '\n' {
			b.unreadByte()
		}
		return tmp // line continuation — no character appended
	case '\n':
		return tmp // line continuation — no character appended
	case '0', '1', '2', '3', '4', '5', '6', '7':
		x := int(c - '0')
		for i := 0; i < 2; i++ {
			c = b.readByte()
			if c < '0' || c > '7' {
				b.unreadByte()
				break
			}
			x = x*8 + int(c-'0')
		}
		if x > 255 {
			b.errorf("invalid octal escape \\%03o", x)
		}
		return append(tmp, byte(x))
	}
}

func (b *buffer) readLiteralString() token {
	tmp := b.tmp[:0]
	depth := 1
Loop:
	for !b.eof {
		c := b.readByte()
		switch c {
		case '(':
			depth++
			tmp = append(tmp, c)
		case ')':
			if depth--; depth == 0 {
				break Loop
			}
			tmp = append(tmp, c)
		case '\\':
			tmp = b.appendEscape(tmp, b.readByte())
		default:
			tmp = append(tmp, c)
		}
	}
	b.tmp = tmp
	return string(tmp)
}

func (b *buffer) readName() token {
	tmp := b.tmp[:0]
	for {
		c := b.readByte()
		if isDelim(c) || isSpace(c) {
			b.unreadByte()
			break
		}
		if c == '#' {
			x := unhex(b.readByte())<<4 | unhex(b.readByte())
			if x < 0 {
				b.errorf("malformed name")
			}
			tmp = append(tmp, byte(x))
			continue
		}
		tmp = append(tmp, c)
	}
	b.tmp = tmp
	return name(string(tmp))
}

func (b *buffer) readKeyword() token {
	tmp := b.tmp[:0]
	for {
		c := b.readByte()
		if isDelim(c) || isSpace(c) {
			b.unreadByte()
			break
		}
		tmp = append(tmp, c)
	}
	b.tmp = tmp
	s := string(tmp)
	switch {
	case s == "true":
		return true
	case s == "false":
		return false
	case isInteger(s):
		x, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			b.errorf("invalid integer %s", s)
		}
		return x
	case isReal(s):
		x, err := strconv.ParseFloat(s, 64)
		if err != nil {
			b.errorf("invalid real %s", s)
		}
		return x
	}
	return keyword(string(tmp))
}

func isInteger(s string) bool {
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || '9' < c {
			return false
		}
	}
	return true
}

func isReal(s string) bool {
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	if len(s) == 0 {
		return false
	}
	ndot := 0
	for _, c := range s {
		if c == '.' {
			ndot++
			continue
		}
		if c < '0' || '9' < c {
			return false
		}
	}
	return ndot == 1
}

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

	if str, ok := tok.(string); ok && b.key != nil && b.objptr.id != 0 {
		tok = decryptString(b.key, b.useAES, b.objptr, str)
	}

	if !b.allowObjptr {
		return tok
	}

	if t1, ok := tok.(int64); ok && int64(uint32(t1)) == t1 {
		if result, matched := b.tryReadObjRef(t1); matched {
			return result
		}
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

	if !b.allowStream {
		return x
	}

	tok := b.readToken()
	if tok != keyword("stream") {
		b.unreadToken(tok)
		return x
	}

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

	return stream{x, b.objptr, b.readOffset()}
}

func isSpace(b byte) bool {
	switch b {
	case '\x00', '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

func isDelim(b byte) bool {
	switch b {
	case '<', '>', '(', ')', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}
