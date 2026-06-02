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

// readAngleBracket dispatches '<' or '>' to the appropriate handler.
func (b *buffer) readAngleBracket(c byte) token {
	if c == '<' {
		return b.readAngleBracketOpen()
	}
	return b.readAngleBracketClose()
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

// readAngleBracketClose handles the '>' case: '>>' becomes the dict-close keyword.
func (b *buffer) readAngleBracketClose() token {
	if b.readByte() == '>' {
		return keyword(">>")
	}
	b.unreadByte()
	b.errorf("unexpected delimiter %#q", rune('>'))
	return nil
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
	case '<', '>':
		return b.readAngleBracket(c)
	case '(':
		return b.readLiteralString()
	case '[', ']', '{', '}':
		return keyword(string(c))
	case '/':
		return b.readName()
	default:
		return b.readTokenDefault(c)
	}
}

func (b *buffer) readTokenDefault(c byte) token {
	if isDelim(c) {
		b.errorf("unexpected delimiter %#q", rune(c))
		return nil
	}
	b.unreadByte()
	return b.readKeyword()
}

// readHexNibble reads the next non-space byte from b that forms part of a hex
// string, skipping whitespace. It returns the byte and true, or 0 and false if
// EOF was reached before a non-space byte was found.
func (b *buffer) readHexNibble() (byte, bool) {
	for {
		c := b.readByte()
		if b.eof {
			return 0, false
		}
		if !isSpace(c) {
			return c, true
		}
	}
}

func (b *buffer) readHexString() token {
	tmp := b.tmp[:0]
	for {
		c, ok := b.readHexNibble()
		if !ok || c == '>' {
			break
		}
		c2, ok := b.readHexNibble()
		if !ok {
			break
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

// namedEscapeByte maps a single-character escape letter to its decoded byte.
// Returns the decoded byte and true if the escape is a recognised named escape.
var namedEscapeByte = map[byte]byte{
	'n': '\n',
	'r': '\r',
	'b': '\b',
	't': '\t',
	'f': '\f',
}

// appendEscape decodes the backslash-escape sequence whose character after
// the backslash is c, appends the decoded bytes to tmp, and returns the
// grown slice. The leading backslash has already been consumed by the caller.
func (b *buffer) appendEscape(tmp []byte, c byte) []byte {
	if decoded, ok := namedEscapeByte[c]; ok {
		return append(tmp, decoded)
	}
	switch c {
	case '(', ')', '\\':
		return append(tmp, c)
	case '\r', '\n':
		return b.skipLineContinuation(tmp, c)
	case '0', '1', '2', '3', '4', '5', '6', '7':
		return b.appendOctalEscape(tmp, c)
	default:
		b.errorf("invalid escape sequence \\%c", c)
		return append(tmp, '\\', c)
	}
}

// skipLineContinuation handles a backslash-newline line continuation.
// For \r, a following \n is consumed as part of the CRLF pair.
func (b *buffer) skipLineContinuation(tmp []byte, c byte) []byte {
	if c == '\r' && b.readByte() != '\n' {
		b.unreadByte()
	}
	return tmp
}

// appendOctalEscape decodes a PDF octal escape \ddd (1–3 octal digits).
// first is the digit already consumed; up to two more are read from b.
func (b *buffer) appendOctalEscape(tmp []byte, first byte) []byte {
	x := int(first - '0')
	for i := 0; i < 2; i++ {
		c := b.readByte()
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
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	if t, ok := b.parseNumericToken(s); ok {
		return t
	}
	return keyword(s)
}

func (b *buffer) parseNumericToken(s string) (token, bool) {
	if isInteger(s) {
		x, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			b.errorf("invalid integer %s", s)
		}
		return x, true
	}
	if isReal(s) {
		x, err := strconv.ParseFloat(s, 64)
		if err != nil {
			b.errorf("invalid real %s", s)
		}
		return x, true
	}
	return nil, false
}

func stripSign(s string) string {
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		return s[1:]
	}
	return s
}

func isInteger(s string) bool {
	s = stripSign(s)
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
	s = stripSign(s)
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
