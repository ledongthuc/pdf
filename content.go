// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"strings"
)

// contentState holds the mutable interpreter state for the Content() operator loop.
type contentState struct {
	g         gstate
	enc       TextEncoding
	text      []Text
	rect      []Rect
	gstack    []gstate
	p         Page
	resources Value
	depth     int
}

// appendText decodes str through the current encoder and appends one Text
// entry per glyph to s.text, advancing the text matrix after each glyph.
func (s *contentState) appendText(str string) {
	n := 0
	decoded := s.enc.Decode(str)
	for _, ch := range decoded {
		var w0 float64
		if n < len(str) {
			w0 = s.g.Tf.Width(int(str[n]))
		}
		n++
		f := s.g.Tf.BaseFont()
		if i := strings.Index(f, "+"); i >= 0 {
			f = f[i+1:]
		}
		Trm := matrix{{s.g.Tfs * s.g.Th, 0, 0}, {0, s.g.Tfs, 0}, {0, s.g.Trise, 1}}.mul(s.g.Tm).mul(s.g.CTM)
		s.text = append(s.text, Text{f, Trm[0][0], Trm[2][0], Trm[2][1], w0 / 1000 * Trm[0][0], string(ch)})
		tx := w0/1000*s.g.Tfs + s.g.Tc
		tx *= s.g.Th
		s.g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(s.g.Tm)
	}
}

// handleGraphics handles path-construction and graphics-state operators.
func (s *contentState) handleGraphics(op string, args []Value) {
	switch op {
	case "cm":
		if len(args) != 6 {
			panic("bad g.Tm")
		}
		s.g.CTM = matrixFrom6Args(args).mul(s.g.CTM)
	case "re":
		if len(args) != 4 {
			panic("bad re")
		}
		x, y, w, h := args[0].Float64(), args[1].Float64(), args[2].Float64(), args[3].Float64()
		s.rect = append(s.rect, Rect{Point{x, y}, Point{x + w, y + h}})
	case "q":
		s.gstack = append(s.gstack, s.g)
	case "Q":
		n := len(s.gstack) - 1
		s.g = s.gstack[n]
		s.gstack = s.gstack[:n]
		// f, g, l, m, cs, scn, gs: no-op
	}
}

func matrixFrom6Args(args []Value) matrix {
	var m matrix
	for i := range 6 {
		m[i/2][i%2] = args[i].Float64()
	}
	m[2][2] = 1
	return m
}

func (s *contentState) applyTd(tx, ty float64) {
	x := matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
	s.g.Tlm = x.mul(s.g.Tlm)
	s.g.Tm = s.g.Tlm
}

// handleTd validates args and moves the text position by (tx, ty).
func (s *contentState) handleTd(args []Value) {
	if len(args) != 2 {
		panic("bad Td")
	}
	s.applyTd(args[0].Float64(), args[1].Float64())
}

// handleTm validates args and sets both text matrices from a 6-element array.
func (s *contentState) handleTm(args []Value) {
	if len(args) != 6 {
		panic("bad g.Tm")
	}
	m := matrixFrom6Args(args)
	s.g.Tm = m
	s.g.Tlm = m
}

// handleTextMatrix handles BT, ET, T*, TD, Td, and Tm operators.
func (s *contentState) handleTextMatrix(op string, args []Value) {
	switch op {
	case "BT":
		s.g.Tm = ident
		s.g.Tlm = s.g.Tm
	case "ET":
		// no-op
	case "T*":
		s.applyTd(0, -s.g.Tl)
	case "TD":
		if len(args) != 2 {
			panic("bad Td")
		}
		s.g.Tl = -args[1].Float64()
		s.handleTd(args)
	case "Td":
		s.handleTd(args)
	case "Tm":
		s.handleTm(args)
	}
}

// handleTf handles the Tf (set text font and size) operator.
func (s *contentState) handleTf(args []Value) {
	if len(args) != 2 {
		panic("bad TL")
	}
	f := args[0].Name()
	s.g.Tf = Font{s.resources.Key("Font").Key(f), nil}
	s.enc = s.g.Tf.Encoder()
	if s.enc == nil {
		if DebugOn {
			println("no cmap for", f)
		}
		s.enc = &nopEncoder{}
	}
	s.g.Tfs = args[1].Float64()
}

func requireOneArg(args []Value, op string) {
	if len(args) != 1 {
		panic("bad " + op)
	}
}

// handleTextParams handles scalar text-state operators: Tc, TL, Tr, Ts, Tw, Tz.
func (s *contentState) handleTextParams(op string, args []Value) {
	requireOneArg(args, op)
	switch op {
	case "Tc":
		s.g.Tc = args[0].Float64()
	case "TL":
		s.g.Tl = args[0].Float64()
	case "Tr":
		s.g.Tmode = int(args[0].Int64())
	case "Ts":
		s.g.Trise = args[0].Float64()
	case "Tw":
		s.g.Tw = args[0].Float64()
	case "Tz":
		s.g.Th = args[0].Float64() / 100
	}
}

// tjSpaceThreshold is the minimum TJ kerning magnitude (in thousandths of a
// text-space unit) that is treated as a word-boundary gap. Values at or beyond
// this threshold cause a synthetic space to be emitted before the next string
// segment. 120 is a conservative word-gap threshold (unidoc/unipdf #524).
const tjSpaceThreshold = 120.0

// interpretTJArray handles the TJ operand array; numeric elements are kerning offsets.
func (s *contentState) interpretTJArray(v Value) {
	needSpace := false
	for i := 0; i < v.Len(); i++ {
		x := v.Index(i)
		if x.Kind() == String {
			if needSpace {
				s.appendText(" ")
				needSpace = false
			}
			s.appendText(x.RawString())
		} else {
			tx := -x.Float64() / 1000 * s.g.Tfs * s.g.Th
			s.g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(s.g.Tm)
			if x.Float64() <= -tjSpaceThreshold {
				needSpace = true
			}
		}
	}
}

// handleTextShow handles text-show operators: Tj, TJ, ', and ".
func (s *contentState) handleTextShow(op string, args []Value) {
	switch op {
	case "\"":
		if len(args) != 3 {
			panic("bad \" operator")
		}
		s.g.Tw = args[0].Float64()
		s.g.Tc = args[1].Float64()
		args = args[2:]
		fallthrough
	case "'":
		if len(args) != 1 {
			panic("bad ' operator")
		}
		s.applyTd(0, -s.g.Tl)
		fallthrough
	case "Tj":
		if len(args) != 1 {
			panic("bad Tj operator")
		}
		s.appendText(args[0].RawString())
	case "TJ":
		s.interpretTJArray(args[0])
		s.appendText("\n")
	}
}

// interpretXObject is the Do operator body; only Form XObjects are walked.
func (s *contentState) interpretXObject(name string) {
	xobj := s.resources.Key("XObject").Key(name)
	if xobj.Key("Subtype").Name() != "Form" {
		return
	}
	sub := &contentState{
		g:         gstate{Th: 1, CTM: ident},
		enc:       &nopEncoder{},
		p:         s.p,
		resources: xobj.Key("Resources"),
		depth:     s.depth + 1,
	}
	Interpret(xobj, sub.interpret)
	s.text = append(s.text, sub.text...)
	s.rect = append(s.rect, sub.rect...)
}

// popArgs drains the stack into a slice, preserving argument order.
func popArgs(stk *Stack) []Value {
	n := stk.Len()
	args := make([]Value, n)
	for i := n - 1; i >= 0; i-- {
		args[i] = stk.Pop()
	}
	return args
}

// handleDo executes the Do operator, walking Form XObjects up to the depth limit.
func (s *contentState) handleDo(args []Value) {
	if s.depth < xobjMaxDepth && len(args) > 0 {
		s.interpretXObject(args[0].Name())
	}
}

// interpret is the per-operator callback passed to Interpret.  It collects
// stack arguments then dispatches to the appropriate handler.
func (s *contentState) interpret(stk *Stack, op string) {
	args := popArgs(stk)
	switch op {
	case "cm", "re", "q", "Q", "f", "g", "l", "m", "cs", "scn", "gs":
		s.handleGraphics(op, args)
	case "BT", "ET", "T*", "TD", "Td", "Tm":
		s.handleTextMatrix(op, args)
	case "Tf":
		s.handleTf(args)
	case "Tc", "TL", "Tr", "Ts", "Tw", "Tz":
		s.handleTextParams(op, args)
	case "Tj", "TJ", "'", "\"":
		s.handleTextShow(op, args)
	case "Do":
		s.handleDo(args)
	}
}

// Content returns the page's content.
func (p Page) Content() Content {
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return Content{}
	}
	s := &contentState{
		g:         gstate{Th: 1, CTM: ident},
		enc:       &nopEncoder{},
		p:         p,
		resources: p.Resources(),
	}
	Interpret(p.V.Key("Contents"), s.interpret)
	return Content{s.text, s.rect}
}
