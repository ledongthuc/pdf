// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
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

// openInterpBuffer builds a buffer from strm, handling both a single stream
// and an Array of streams (concatenated via io.MultiReader).
func openInterpBuffer(strm Value) *buffer {
	var b *buffer
	if strm.Kind() == Array {
		n := strm.Len()
		readers := make([]io.Reader, n)
		for i := 0; i < n; i++ {
			readers[i] = strm.Index(i).Reader()
		}
		b = newBuffer(io.MultiReader(readers...), 0)
	} else {
		b = newBuffer(strm.Reader(), 0)
	}
	b.allowEOF = true
	b.allowObjptr = false
	b.allowStream = false
	return b
}

// execDef implements the PostScript "def" operator: pops a name key and a
// value, then stores the pair in the top dictionary. Returns false without
// storing if the key is not a name (silent skip per PS semantics).
func execDef(stk *Stack, dicts *[]dict) {
	if len(*dicts) <= 0 {
		panic("def without open dict")
	}
	val := stk.Pop()
	key, ok := stk.Pop().data.(name)
	if !ok {
		return
	}
	(*dicts)[len(*dicts)-1][key] = val.data
}

func psDict(stk *Stack) {
	stk.Pop()
	stk.Push(Value{nil, objptr{}, make(dict)})
}

func psCurrentdict(stk *Stack, dicts *[]dict) {
	if len(*dicts) == 0 {
		panic("no current dictionary")
	}
	stk.Push(Value{nil, objptr{}, (*dicts)[len(*dicts)-1]})
}

func psBegin(stk *Stack, dicts *[]dict) {
	d := stk.Pop()
	if d.Kind() != Dict {
		panic("cannot begin non-dict")
	}
	*dicts = append(*dicts, d.data.(dict))
}

func psEnd(dicts *[]dict) {
	if len(*dicts) <= 0 {
		panic("mismatched begin/end")
	}
	*dicts = (*dicts)[:len(*dicts)-1]
}

// execPS handles the built-in PostScript dict-stack operators (dict,
// currentdict, begin, end, def, pop). Returns true if kw was consumed,
// false if it is not a PS dict operator and must be dispatched to the
// caller's do function.
func execPS(kw string, stk *Stack, dicts *[]dict) bool {
	switch kw {
	case "dict":
		psDict(stk)
	case "currentdict":
		psCurrentdict(stk, dicts)
	case "begin":
		psBegin(stk, dicts)
	case "end":
		psEnd(dicts)
	case "def":
		execDef(stk, dicts)
	case "pop":
		stk.Pop()
	default:
		return false
	}
	return true
}

// eiKeywordTerminates reads the byte after "EI" and returns true if that byte
// confirms a keyword boundary (whitespace, delimiter, or EOF). It unreads the
// byte when it does not consume it, leaving the buffer position correct for
// either outcome.
func eiKeywordTerminates(b *buffer) bool {
	c := b.readByte()
	if b.eof {
		return true
	}
	if isSpace(c) || isDelim(c) {
		b.unreadByte()
		return true
	}
	b.unreadByte()
	return false
}

// skipInlineImage scans past inline image binary data until the EI keyword.
// Inline image: binary pixel data follows ID until the EI keyword.
// Scan byte-by-byte; calling readToken on raw binary would feed
// the lexer arbitrary bytes (e.g. 0x3c triggering readHexString)
// and loop indefinitely.  Per PDF spec §8.9.7, EI must be
// preceded by a whitespace character.
func skipInlineImage(b *buffer) {
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
		if eiKeywordTerminates(b) {
			break
		}
	}
}

// lookupInDicts searches dicts from innermost to outermost for kw and pushes
// the found value onto stk. Returns true if found, false otherwise.
func lookupInDicts(kw keyword, stk *Stack, dicts []dict) bool {
	for i := len(dicts) - 1; i >= 0; i-- {
		if v, ok := dicts[i][name(kw)]; ok {
			stk.Push(Value{nil, objptr{}, v})
			return true
		}
	}
	return false
}

// dispatchKeyword executes one keyword token from the PostScript stream.
// Returns true if the caller's main loop should continue to the next token
// (i.e. a dict lookup matched and the value was pushed), false to fall through.
func dispatchKeyword(kw keyword, stk *Stack, dicts *[]dict, b *buffer, do func(stk *Stack, op string)) bool {
	switch kw {
	// "null", "[", "]", "<<", ">>" are PDF structural tokens that must be
	// re-read as full objects via readObject — do not dispatch to do() or execPS.
	case "null", "[", "]", "<<", ">>":
		b.unreadToken(kw)
		stk.Push(Value{nil, objptr{}, b.readObject()})
	case "ID":
		skipInlineImage(b)
		do(stk, "EI")
	default:
		if execPS(string(kw), stk, dicts) {
			return false
		}
		if lookupInDicts(kw, stk, *dicts) {
			return true
		}
		do(stk, string(kw))
	}
	return false
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
	b := openInterpBuffer(strm)

	for {
		tok := b.readToken()
		if tok == io.EOF {
			break
		}
		kw, ok := tok.(keyword)
		if !ok {
			b.unreadToken(tok)
			stk.Push(Value{nil, objptr{}, b.readObject()})
			continue
		}
		dispatchKeyword(kw, &stk, &dicts, b, do)
	}
}
