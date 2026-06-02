// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

// walkState holds mutable state for the walkTextBlocks Interpret callback.
type walkState struct {
	enc       TextEncoding
	x, y      float64
	tl        float64
	fonts     map[string]*Font
	walker    func(TextEncoding, float64, float64, string)
	resources Value
	depth     int
}

func (s *walkState) handleWalkFont(op string, args []Value) {
	switch op {
	case "BT":
		s.x = 0
		s.y = 0
	case "T*":
		s.y -= s.tl
	case "TL":
		if len(args) != 1 {
			return
		}
		s.tl = args[0].Float64()
	case "Tf":
		if len(args) != 2 {
			panic("bad TL")
		}
		if font, ok := s.fonts[args[0].Name()]; ok {
			s.enc = font.Encoder()
		} else {
			s.enc = &nopEncoder{}
		}
	}
}

func (s *walkState) handleWalkShow(op string, args []Value) {
	switch op {
	case "\"":
		if len(args) != 3 {
			panic("bad \" operator")
		}
		fallthrough
	case "'":
		if len(args) != 1 {
			panic("bad ' operator")
		}
		fallthrough
	case "Tj":
		if len(args) != 1 {
			panic("bad Tj operator")
		}
		s.walker(s.enc, s.x, s.y, args[0].RawString())
	case "TJ":
		v := args[0]
		for i := 0; i < v.Len(); i++ {
			x := v.Index(i)
			if x.Kind() == String {
				s.walker(s.enc, s.x, s.y, x.RawString())
			}
		}
	}
}

func (s *walkState) handleWalkPos(op string, args []Value) {
	switch op {
	case "Td":
		if len(args) != 2 {
			return
		}
		s.x += args[0].Float64()
		s.y += args[1].Float64()
	case "TD":
		if len(args) != 2 {
			return
		}
		ty := args[1].Float64()
		s.x += args[0].Float64()
		s.y += ty
		s.tl = -ty
	case "Tm":
		s.x = args[4].Float64()
		s.y = args[5].Float64()
	}
}

func (s *walkState) interpretWalk(stk *Stack, op string) {
	n := stk.Len()
	args := make([]Value, n)
	for i := n - 1; i >= 0; i-- {
		args[i] = stk.Pop()
	}
	switch op {
	case "BT", "T*", "TL", "Tf":
		s.handleWalkFont(op, args)
	case "\"", "'", "Tj", "TJ":
		s.handleWalkShow(op, args)
	case "Td", "TD", "Tm":
		s.handleWalkPos(op, args)
	case "Do":
		if s.depth >= xobjMaxDepth || len(args) == 0 {
			break
		}
		xobj := s.resources.Key("XObject").Key(args[0].Name())
		if xobj.Key("Subtype").Name() != "Form" {
			break
		}
		xobjRes := xobj.Key("Resources")
		sub := &walkState{enc: &nopEncoder{}, resources: xobjRes, depth: s.depth + 1, walker: s.walker}
		sub.fonts = make(map[string]*Font)
		for _, fn := range xobjRes.Key("Font").Keys() {
			f := Font{xobjRes.Key("Font").Key(fn), nil}
			sub.fonts[fn] = &f
		}
		Interpret(xobj, sub.interpretWalk)
	}
}

func (p Page) walkTextBlocks(walker func(enc TextEncoding, x, y float64, s string)) {
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return
	}
	fonts := make(map[string]*Font)
	for _, font := range p.Fonts() {
		f := p.Font(font)
		fonts[font] = &f
	}
	s := &walkState{enc: &nopEncoder{}, fonts: fonts, walker: walker, resources: p.Resources()}
	Interpret(p.V.Key("Contents"), s.interpretWalk)
}
