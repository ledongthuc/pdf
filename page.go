// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
)

// A Page represent a single page in a PDF file.
// The methods interpret a Page dictionary stored in V.
type Page struct {
	V Value
}

// Page returns the page for the given page number.
// Page numbers are indexed starting at 1, not 0.
// If the page is not found, Page returns a Page with p.V.IsNull().
func (r *Reader) Page(num int) Page {
	num-- // now 0-indexed
	page := r.Trailer().Key("Root").Key("Pages")
Search:
	for page.Key("Type").Name() == "Pages" {
		count := int(page.Key("Count").Int64())
		if count < num {
			return Page{}
		}
		kids := page.Key("Kids")
		for i := 0; i < kids.Len(); i++ {
			kid := kids.Index(i)
			if kid.Key("Type").Name() == "Pages" {
				c := int(kid.Key("Count").Int64())
				if num < c {
					page = kid
					continue Search
				}
				num -= c
				continue
			}
			if kid.Key("Type").Name() == "Page" {
				if num == 0 {
					return Page{kid}
				}
				num--
			}
		}
		break
	}
	return Page{}
}

// NumPage returns the number of pages in the PDF file.
func (r *Reader) NumPage() int {
	return int(r.Trailer().Key("Root").Key("Pages").Key("Count").Int64())
}

// GetPlainText returns all the text in the PDF file
func (r *Reader) GetPlainText() (reader io.Reader, err error) {
	pages := r.NumPage()
	var buf bytes.Buffer
	for i := 1; i <= pages; i++ {
		p := r.Page(i)
		text, err := p.GetPlainText(nil)
		if err != nil {
			return &bytes.Buffer{}, err
		}
		buf.WriteString(text)
	}
	return &buf, nil
}

// GetStyledTexts returns list all sentences in an array, that are included styles
func (r *Reader) GetStyledTexts() (sentences []Text, err error) {
	totalPage := r.NumPage()
	for pageIndex := 1; pageIndex <= totalPage; pageIndex++ {
		p := r.Page(pageIndex)

		if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
			continue
		}
		var lastTextStyle Text
		texts := p.Content().Text
		for _, text := range texts {
			if lastTextStyle == (Text{}) {
				lastTextStyle = text
				continue
			}

			if IsSameSentence(lastTextStyle, text) {
				lastTextStyle.S = lastTextStyle.S + text.S
			} else {
				sentences = append(sentences, lastTextStyle)
				lastTextStyle = text
			}
		}
		if len(lastTextStyle.S) > 0 {
			sentences = append(sentences, lastTextStyle)
		}
	}

	return sentences, err
}

// Pages returns an iterator over all pages in the PDF, yielding the 1-based
// page index and the Page value. Break exits cleanly with no goroutine leak.
func (r *Reader) Pages() iter.Seq2[int, Page] {
	return func(yield func(int, Page) bool) {
		n := r.NumPage()
		for i := 1; i <= n; i++ {
			if !yield(i, r.Page(i)) {
				return
			}
		}
	}
}

// Texts returns an iterator over the styled text elements on the page,
// merging adjacent runs that share the same style (font, size, position),
// matching the output of (*Reader).GetStyledTexts. Break exits cleanly.
func (p Page) Texts() iter.Seq[Text] {
	return func(yield func(Text) bool) {
		var last Text
		for _, text := range p.Content().Text {
			if last == (Text{}) {
				last = text
				continue
			}
			if IsSameSentence(last, text) {
				last.S = last.S + text.S
			} else {
				if !yield(last) {
					return
				}
				last = text
			}
		}
		if len(last.S) > 0 {
			yield(last)
		}
	}
}

func (p Page) findInherited(key string) Value {
	for v := p.V; !v.IsNull(); v = v.Key("Parent") {
		if r := v.Key(key); !r.IsNull() {
			return r
		}
	}
	return Value{}
}

func rectFromValue(v Value) [4]float64 {
	return [4]float64{
		v.Index(0).Float64(),
		v.Index(1).Float64(),
		v.Index(2).Float64(),
		v.Index(3).Float64(),
	}
}

func (p Page) MediaBox() [4]float64 {
	return rectFromValue(p.findInherited("MediaBox"))
}

func (p Page) CropBox() [4]float64 {
	if r := p.findInherited("CropBox"); r.Kind() != Null {
		return rectFromValue(r)
	}
	return p.MediaBox()
}

// Resources returns the resources dictionary associated with the page.
func (p Page) Resources() Value {
	return p.findInherited("Resources")
}

// Fonts returns a list of the fonts associated with the page.
func (p Page) Fonts() []string {
	return p.Resources().Key("Font").Keys()
}

// Font returns the font with the given name associated with the page.
func (p Page) Font(name string) Font {
	return Font{p.Resources().Key("Font").Key(name), nil}
}

type matrix [3][3]float64

var ident = matrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (x matrix) mul(y matrix) matrix {
	var z matrix
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				z[i][j] += x[i][k] * y[k][j]
			}
		}
	}
	return z
}

type gstate struct {
	Tc    float64
	Tw    float64
	Th    float64
	Tl    float64
	Tf    Font
	Tfs   float64
	Tmode int
	Trise float64
	Tm    matrix
	Tlm   matrix
	Trm   matrix
	CTM   matrix
}

// plainTextState holds mutable state for the GetPlainText Interpret callback.
// xobjMaxDepth caps Form XObject recursion to guard against malformed PDFs
// that contain cyclic or deeply nested XObject references.
const xobjMaxDepth = 10

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
		var m matrix
		for i := 0; i < 6; i++ {
			m[i/2][i%2] = args[i].Float64()
		}
		m[2][2] = 1
		s.g.CTM = m.mul(s.g.CTM)
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

// handleTextMatrix handles BT, ET, T*, TD, Td, and Tm operators.
func (s *contentState) handleTextMatrix(op string, args []Value) {
	switch op {
	case "BT":
		s.g.Tm = ident
		s.g.Tlm = s.g.Tm
	case "ET":
		// no-op
	case "T*":
		x := matrix{{1, 0, 0}, {0, 1, 0}, {0, -s.g.Tl, 1}}
		s.g.Tlm = x.mul(s.g.Tlm)
		s.g.Tm = s.g.Tlm
	case "TD":
		if len(args) != 2 {
			panic("bad Td")
		}
		s.g.Tl = -args[1].Float64()
		fallthrough
	case "Td":
		if len(args) != 2 {
			panic("bad Td")
		}
		tx := args[0].Float64()
		ty := args[1].Float64()
		x := matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
		s.g.Tlm = x.mul(s.g.Tlm)
		s.g.Tm = s.g.Tlm
	case "Tm":
		if len(args) != 6 {
			panic("bad g.Tm")
		}
		var m matrix
		for i := 0; i < 6; i++ {
			m[i/2][i%2] = args[i].Float64()
		}
		m[2][2] = 1
		s.g.Tm = m
		s.g.Tlm = m
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

// handleTextParams handles scalar text-state operators: Tc, TL, Tr, Ts, Tw, Tz.
func (s *contentState) handleTextParams(op string, args []Value) {
	switch op {
	case "Tc":
		if len(args) != 1 {
			panic("bad g.Tc")
		}
		s.g.Tc = args[0].Float64()
	case "TL":
		if len(args) != 1 {
			panic("bad TL")
		}
		s.g.Tl = args[0].Float64()
	case "Tr":
		if len(args) != 1 {
			panic("bad Tr")
		}
		s.g.Tmode = int(args[0].Int64())
	case "Ts":
		if len(args) != 1 {
			panic("bad Ts")
		}
		s.g.Trise = args[0].Float64()
	case "Tw":
		if len(args) != 1 {
			panic("bad g.Tw")
		}
		s.g.Tw = args[0].Float64()
	case "Tz":
		if len(args) != 1 {
			panic("bad Tz")
		}
		s.g.Th = args[0].Float64() / 100
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
		x := matrix{{1, 0, 0}, {0, 1, 0}, {0, -s.g.Tl, 1}}
		s.g.Tlm = x.mul(s.g.Tlm)
		s.g.Tm = s.g.Tlm
		fallthrough
	case "Tj":
		if len(args) != 1 {
			panic("bad Tj operator")
		}
		s.appendText(args[0].RawString())
	case "TJ":
		v := args[0]
		for i := 0; i < v.Len(); i++ {
			x := v.Index(i)
			if x.Kind() == String {
				s.appendText(x.RawString())
			} else {
				tx := -x.Float64() / 1000 * s.g.Tfs * s.g.Th
				s.g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(s.g.Tm)
			}
		}
		s.appendText("\n")
	}
}

// interpret is the per-operator callback passed to Interpret.  It collects
// stack arguments then dispatches to the appropriate handler.
func (s *contentState) interpret(stk *Stack, op string) {
	n := stk.Len()
	args := make([]Value, n)
	for i := n - 1; i >= 0; i-- {
		args[i] = stk.Pop()
	}
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
		if s.depth >= xobjMaxDepth || len(args) == 0 {
			break
		}
		xobj := s.resources.Key("XObject").Key(args[0].Name())
		if xobj.Key("Subtype").Name() != "Form" {
			break
		}
		xobjRes := xobj.Key("Resources")
		sub := &contentState{
			g:         gstate{Th: 1, CTM: ident},
			enc:       &nopEncoder{},
			p:         s.p,
			resources: xobjRes,
			depth:     s.depth + 1,
		}
		Interpret(xobj, sub.interpret)
		s.text = append(s.text, sub.text...)
		s.rect = append(s.rect, sub.rect...)
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
