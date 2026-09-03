// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"io"
	"runtime"
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
	var rd io.Reader
	if strm.Kind() == Array {
		readers := make([]io.Reader, 0, strm.Len())
		for i := 0; i < strm.Len(); i++ {
			readers = append(readers, strm.Index(i).Reader())
		}
		rd = io.MultiReader(readers...)
	} else {
		rd = strm.Reader()
	}

	b := newBuffer(rd, 0)
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
				val := stk.Pop()
				if len(dicts) <= 0 {
					// A "def" with no dict opened by "begin" is invalid
					// PostScript, but producers emit it in practice inside a
					// malformed CMap dictionary LITERAL (e.g.
					// "<</Registry (x) def/Ordering (y) def>>", where "def"
					// should not appear between "<<" and ">>" at all). This
					// package is a limited PostScript subset for embedded
					// CMap/function data, not a strict validator (see the
					// doc comment above), so discard the operand and keep
					// going rather than take the whole Interpret call down
					// over one producer's malformed dict.
					continue
				}
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
			}
		}
		b.unreadToken(tok)
		obj, ok := readObjectRecover(b)
		if !ok {
			continue
		}
		stk.Push(Value{nil, objptr{}, obj})
	}
}

// readObjectRecover reads one object, recovering a panic from malformed
// input rather than letting it escape Interpret.
//
// Interpret parses an embedded PostScript-SUBSET stream (a CMap, a function)
// that is not always a well-formed PDF object graph — for example, a
// producer's CMap embedding "def" tokens inside what should be a plain
// dictionary literal, which readObject (correctly, for a real PDF object)
// treats as a hard parse error. Letting that escape takes the WHOLE calling
// operation down — e.g. reading a font's /ToUnicode CMap — over one
// unparseable operand, even when the caller (readCmap) only needs the
// recognized cmap operators and the rest of the stream is fine. ok=false
// means the operand is discarded; the Reading loop continues from wherever
// the underlying buffer's position landed.
func readObjectRecover(b *buffer) (obj object, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			// Parse errors are raised with panic(fmt.Errorf(...)) and mean
			// "discard this operand and keep going". Anything else (nil
			// deref, index out of range, ...) is a genuine bug and must not
			// be silently swallowed as malformed input.
			if _, isRuntime := r.(runtime.Error); isRuntime {
				panic(r)
			}
			obj, ok = nil, false
		}
	}()
	return b.readObject(), true
}
