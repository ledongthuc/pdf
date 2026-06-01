// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"fmt"
	"io"
)

// A Stack represents a stack of values.
type Stack struct {
	stack []Value
}

func (stk *Stack) Len() int {
	return len(stk.stack)
}

func (stk *Stack) Push(v Value) {
	stk.stack = append(stk.stack, v)
}

func (stk *Stack) Pop() Value {
	n := len(stk.stack)
	if n == 0 {
		return Value{}
	}
	v := stk.stack[n-1]
	stk.stack[n-1] = Value{}
	stk.stack = stk.stack[:n-1]
	return v
}

func newDict() Value {
	return Value{nil, objptr{}, make(dict)}
}

// Interpret interprets the content in a stream as a basic PostScript program,
// pushing values onto a stack and then calling the do function to execute
// operators. The do function may push or pop values from the stack as needed
// to implement op.
//
// Interpret handles the operators "dict", "currentdict", "begin", "end", "def", and "pop" itself.
//
// Interpret is not a full-blown PostScript interpreter. Its job is to handle the
// very limited PostScript found in certain supporting file formats embedded
// in PDF files, such as cmap files that describe the mapping from font code
// points to Unicode code points.
//
// A stream can also be represented by an array of streams that has to be handled as a single stream
// In the case of a simple stream read only once, otherwise get the length of the stream to handle it properly
//
// There is no support for executable blocks, among other limitations.
func Interpret(strm Value, do func(stk *Stack, op string)) {
	var stk Stack
	var dicts []dict
	var b *buffer
	if strm.Kind() == Array {
		strmlen := strm.Len()
		streams := make([]io.Reader, 0, strmlen)
		for i := 0; i < strmlen; i++ {
			streams = append(streams, strm.Index(i).Reader())
		}
		multiReader := io.MultiReader(streams...)
		b = newBuffer(multiReader, 0)
	} else {
		rd := strm.Reader()
		b = newBuffer(rd, 0)
	}
	b.allowEOF = true
	b.allowObjptr = false
	b.allowStream = false

Reading:
	for {
		tok := b.readToken()
		if tok == io.EOF {
			break
		}
		if kw, ok := tok.(keyword); ok {
			switch kw {
			case "null", "[", "]", "<<", ">>":
				break
			default:
				for i := len(dicts) - 1; i >= 0; i-- {
					if v, ok := dicts[i][name(kw)]; ok {
						stk.Push(Value{nil, objptr{}, v})
						continue Reading
					}
				}
				do(&stk, string(kw))
				continue
			case "dict":
				stk.Pop()
				stk.Push(Value{nil, objptr{}, make(dict)})
				continue
			case "currentdict":
				if len(dicts) == 0 {
					panic("no current dictionary")
				}
				stk.Push(Value{nil, objptr{}, dicts[len(dicts)-1]})
				continue
			case "begin":
				d := stk.Pop()
				if d.Kind() != Dict {
					panic("cannot begin non-dict")
				}
				dicts = append(dicts, d.data.(dict))
				continue
			case "end":
				if len(dicts) <= 0 {
					panic("mismatched begin/end")
				}
				dicts = dicts[:len(dicts)-1]
				continue
			case "def":
				if len(dicts) <= 0 {
					panic("def without open dict")
				}
				val := stk.Pop()
				key, ok := stk.Pop().data.(name)
				if !ok {
					// panic(fmt.Sprintf("def of non-name: %+v", stk.Pop().data))
					// Skip the value if it has key without value
					continue
				}
				dicts[len(dicts)-1][key] = val.data
				continue
			case "pop":
				stk.Pop()
				continue
			case "ID":
				// Inline image: binary pixel data follows until the EI keyword.
				// Scan byte-by-byte; calling readToken on raw binary would feed
				// the lexer arbitrary bytes (e.g. 0x3c triggering readHexString)
				// and loop indefinitely.  Per PDF spec §8.9.7, EI must be
				// preceded by a whitespace character.
				for !b.eof {
					c := b.readByte()
					if c != 'E' {
						continue
					}
					c2 := b.readByte()
					if b.eof {
						break
					}
					if c2 != 'I' {
						b.unreadByte()
						continue
					}
					// Verify the two-char sequence is truly the EI keyword
					// (followed by whitespace, a delimiter, or EOF).
					c3 := b.readByte()
					if b.eof || isSpace(c3) || isDelim(c3) {
						if !b.eof {
							b.unreadByte()
						}
						break
					}
					// False positive inside image data; keep scanning.
					b.unreadByte()
				}
				do(&stk, "EI")
				continue
			}
		}
		b.unreadToken(tok)
		obj := b.readObject()
		stk.Push(Value{nil, objptr{}, obj})
	}
}

type seqReader struct {
	rd     io.Reader
	offset int64
}

func (r *seqReader) ReadAt(buf []byte, offset int64) (int, error) {
	if offset != r.offset {
		return 0, fmt.Errorf("non-sequential read of stream")
	}
	n, err := io.ReadFull(r.rd, buf)
	r.offset += int64(n)
	return n, err
}
