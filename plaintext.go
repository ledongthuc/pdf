// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"bytes"
	"errors"
	"fmt"
)

// plainTextState holds mutable state for the GetPlainText Interpret callback.
type plainTextState struct {
	enc       TextEncoding
	fonts     map[string]*Font
	buf       bytes.Buffer
	resources Value
	depth     int
}

func (s *plainTextState) showEncoded(str string) {
	for _, ch := range s.enc.Decode(str) {
		_, err := s.buf.WriteRune(ch)
		if err != nil {
			panic(err)
		}
	}
}

func (s *plainTextState) handlePlainTf(args []Value) {
	if len(args) != 2 {
		panic("bad TL")
	}
	if font, ok := s.fonts[args[0].Name()]; ok {
		s.enc = font.Encoder()
	} else {
		s.enc = &nopEncoder{}
	}
}

func (s *plainTextState) handlePlainShow(op string, args []Value) {
	switch op {
	case "BT":
	case "T*":
		s.showEncoded("\n")
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
		s.showEncoded(args[0].RawString())
	case "TJ":
		v := args[0]
		for i := 0; i < v.Len(); i++ {
			x := v.Index(i)
			if x.Kind() == String {
				s.showEncoded(x.RawString())
			}
		}
	}
}

func (s *plainTextState) interpretPlain(stk *Stack, op string) {
	n := stk.Len()
	args := make([]Value, n)
	for i := n - 1; i >= 0; i-- {
		args[i] = stk.Pop()
	}
	switch op {
	case "Tf":
		s.handlePlainTf(args)
	case "BT", "T*", "\"", "'", "Tj", "TJ":
		s.handlePlainShow(op, args)
	case "Do":
		if s.depth >= xobjMaxDepth || len(args) == 0 {
			break
		}
		xobj := s.resources.Key("XObject").Key(args[0].Name())
		if xobj.Key("Subtype").Name() != "Form" {
			break
		}
		xobjRes := xobj.Key("Resources")
		sub := &plainTextState{enc: &nopEncoder{}, resources: xobjRes, depth: s.depth + 1}
		sub.fonts = make(map[string]*Font)
		for _, fn := range xobjRes.Key("Font").Keys() {
			f := Font{xobjRes.Key("Font").Key(fn), nil}
			sub.fonts[fn] = &f
		}
		Interpret(xobj, sub.interpretPlain)
		s.buf.WriteString(sub.buf.String())
	}
}

// GetPlainText returns the page's all text without format.
// fonts can be passed in (to improve parsing performance) or left nil
func (p Page) GetPlainText(fonts map[string]*Font) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = ""
			err = errors.New(fmt.Sprint(r))
		}
	}()

	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return "", nil
	}
	if fonts == nil {
		fonts = make(map[string]*Font)
		for _, font := range p.Fonts() {
			f := p.Font(font)
			fonts[font] = &f
		}
	}
	s := &plainTextState{enc: &nopEncoder{}, fonts: fonts, resources: p.Resources()}
	Interpret(p.V.Key("Contents"), s.interpretPlain)
	return s.buf.String(), nil
}
