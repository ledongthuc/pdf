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
	"sort"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
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

// A Font represent a font in a PDF file.
// The methods interpret a Font dictionary stored in V.
type Font struct {
	V   Value
	enc TextEncoding
}

// BaseFont returns the font's name (BaseFont property).
func (f Font) BaseFont() string {
	return f.V.Key("BaseFont").Name()
}

// FirstChar returns the code point of the first character in the font.
func (f Font) FirstChar() int {
	return int(f.V.Key("FirstChar").Int64())
}

// LastChar returns the code point of the last character in the font.
func (f Font) LastChar() int {
	return int(f.V.Key("LastChar").Int64())
}

// Widths returns the widths of the glyphs in the font.
// In a well-formed PDF, len(f.Widths()) == f.LastChar()+1 - f.FirstChar().
func (f Font) Widths() []float64 {
	x := f.V.Key("Widths")
	var out []float64
	for i := 0; i < x.Len(); i++ {
		out = append(out, x.Index(i).Float64())
	}
	return out
}

// Width returns the width of the given code point.
func (f Font) Width(code int) float64 {
	first := f.FirstChar()
	last := f.LastChar()
	if code < first || last < code {
		return 0
	}
	return f.V.Key("Widths").Index(code - first).Float64()
}

// Encoder returns the encoding between font code point sequences and UTF-8.
func (f Font) Encoder() TextEncoding {
	if f.enc == nil { // caching the Encoder so we don't have to continually parse charmap
		f.enc = f.getEncoder()
	}
	return f.enc
}

// encoderForCMapName returns the TextEncoding for a named PDF CMap/Encoding.
func encoderForCMapName(n string) TextEncoding {
	switch n {
	case "WinAnsiEncoding":
		return &byteEncoder{&winAnsiEncoding}
	case "MacRomanEncoding":
		return &byteEncoder{&macRomanEncoding}
	case "Identity-H":
		return &byteEncoder{&pdfDocEncoding}
	case "90ms-RKSJ-H", "90ms-RKSJ-V", "90pv-RKSJ-H":
		return &multibyteCMapEncoder{japanese.ShiftJIS}
	case "UniGB-UCS2-H", "UniGB-UCS2-V",
		"UniCNS-UCS2-H", "UniCNS-UCS2-V",
		"UniJIS-UCS2-H", "UniJIS-UCS2-V",
		"UniKS-UCS2-H", "UniKS-UCS2-V":
		return &ucs2BEEncoder{}
	case "GB-EUC-H", "GB-EUC-V",
		"GBKp-EUC-H", "GBKp-EUC-V",
		"GBK-EUC-H", "GBK-EUC-V":
		return &multibyteCMapEncoder{simplifiedchinese.GBK}
	case "ETen-B5-H", "ETen-B5-V",
		"ETenms-B5-H", "ETenms-B5-V":
		return &multibyteCMapEncoder{traditionalchinese.Big5}
	case "KSCms-UHC-H", "KSCms-UHC-V",
		"KSC-EUC-H", "KSC-EUC-V",
		"KSCms-UHC-HW-H", "KSCms-UHC-HW-V":
		return &multibyteCMapEncoder{korean.EUCKR}
	default:
		if DebugOn {
			println("unknown encoding", n)
		}
		return &byteEncoder{&pdfDocEncoding}
	}
}

func (f Font) getEncoder() TextEncoding {
	toUnicode := f.V.Key("ToUnicode")
	if toUnicode.Kind() == Stream {
		if m := readCmap(toUnicode); m != nil {
			return m
		}
		if DebugOn {
			println("ToUnicode stream failed to parse, falling back to Encoding")
		}
	}
	enc := f.V.Key("Encoding")
	switch enc.Kind() {
	case Name:
		return encoderForCMapName(enc.Name())
	case Dict:
		return newDictEncoder(enc)
	case Null:
		return &byteEncoder{&pdfDocEncoding}
	default:
		if DebugOn {
			println("unexpected encoding", enc.String())
		}
		return &byteEncoder{&pdfDocEncoding}
	}
}

// dictEncoder handles fonts with Encoding dictionaries containing
// BaseEncoding and/or Differences arrays per PDF spec section 9.6.6.
type dictEncoder struct {
	table [256]rune
}

func newDictEncoder(enc Value) *dictEncoder {
	e := &dictEncoder{}
	baseEnc := enc.Key("BaseEncoding")
	var baseTable *[256]rune
	switch baseEnc.Name() {
	case "WinAnsiEncoding":
		baseTable = &winAnsiEncoding
	case "MacRomanEncoding":
		baseTable = &macRomanEncoding
	default:
		baseTable = &pdfDocEncoding
	}
	copy(e.table[:], baseTable[:])

	diff := enc.Key("Differences")
	if diff.Kind() == Array {
		code := -1
		for j := 0; j < diff.Len(); j++ {
			x := diff.Index(j)
			if x.Kind() == Integer {
				code = int(x.Int64())
				continue
			}
			if x.Kind() == Name && code >= 0 && code < 256 {
				if r := nameToRune[x.Name()]; r != 0 {
					e.table[code] = r
				}
				code++
			}
		}
	}
	return e
}

func (e *dictEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		r = append(r, e.table[raw[i]])
	}
	return string(r)
}

// A TextEncoding represents a mapping between
// font code points and UTF-8 text.
type TextEncoding interface {
	// Decode returns the UTF-8 text corresponding to
	// the sequence of code points in raw.
	Decode(raw string) (text string)
}

type nopEncoder struct {
}

func (e *nopEncoder) Decode(raw string) (text string) {
	return raw
}

// multibyteCMapEncoder decodes PDF content-stream bytes using an x/text Encoding.
// Used for predefined CMaps whose raw bytes are a well-known legacy encoding
// (e.g. Shift-JIS for 90ms-RKSJ-H). Silently falls back to raw bytes on error.
type multibyteCMapEncoder struct {
	enc encoding.Encoding
}

func (e *multibyteCMapEncoder) Decode(raw string) (text string) {
	decoded, err := e.enc.NewDecoder().Bytes([]byte(raw))
	if err != nil {
		return raw
	}
	return string(decoded)
}

// ucs2BEEncoder decodes PDF content-stream bytes encoded as UCS-2 big-endian.
// Used for predefined CMaps such as UniGB-UCS2-H/V, UniCNS-UCS2-H/V,
// UniJIS-UCS2-H/V, and UniKS-UCS2-H/V. Each glyph selector is a 2-byte
// big-endian Unicode code point (e.g. 中 = 0x4E2D). No external dependency needed.
// UCS-2/BMP-only — no surrogate-pair handling; correct for Uni*-UCS2-* CMaps.
type ucs2BEEncoder struct{}

func (e *ucs2BEEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		r = append(r, rune(uint16(raw[i])<<8|uint16(raw[i+1])))
	}
	return string(r)
}

type byteEncoder struct {
	table *[256]rune
}

func (e *byteEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		r = append(r, e.table[raw[i]])
	}
	return string(r)
}

type byteRange struct {
	low  string
	high string
}

type bfchar struct {
	orig string
	repl string
}

type bfrange struct {
	lo  string
	hi  string
	dst Value
}

type cmap struct {
	space   [4][]byteRange // codespace range
	bfrange []bfrange
	bfchar  []bfchar
}

// lookupBfchar searches m.bfchar for an entry of length n matching text.
func (m *cmap) lookupBfchar(text string, n int) ([]rune, bool) {
	for _, bfchar := range m.bfchar {
		if len(bfchar.orig) == n && bfchar.orig == text {
			return []rune(utf16Decode(bfchar.repl)), true
		}
	}
	return nil, false
}

// lookupBfrange searches m.bfrange for an entry of length n whose range contains text.
func (m *cmap) lookupBfrange(text string, n int) ([]rune, bool) {
	for _, bfrange := range m.bfrange {
		if len(bfrange.lo) == n && bfrange.lo <= text && text <= bfrange.hi {
			if bfrange.dst.Kind() == String {
				s := bfrange.dst.RawString()
				if bfrange.lo != text {
					b := []byte(s)
					b[len(b)-1] += text[len(text)-1] - bfrange.lo[len(bfrange.lo)-1]
					s = string(b)
				}
				return []rune(utf16Decode(s)), true
			}
			if bfrange.dst.Kind() == Array {
				idx := text[len(text)-1] - bfrange.lo[len(bfrange.lo)-1]
				v := bfrange.dst.Index(int(idx))
				if v.Kind() == String {
					return []rune(utf16Decode(v.RawString())), true
				}
				if DebugOn {
					fmt.Printf("array %v\n", bfrange.dst)
				}
			} else {
				if DebugOn {
					fmt.Printf("unknown dst %v\n", bfrange.dst)
				}
			}
			return []rune{noRune}, true
		}
	}
	return nil, false
}

func (m *cmap) Decode(raw string) (text string) {
	var r []rune
Parse:
	for len(raw) > 0 {
		for n := 1; n <= 4 && n <= len(raw); n++ {
			for _, space := range m.space[n-1] {
				if space.low <= raw[:n] && raw[:n] <= space.high {
					text := raw[:n]
					raw = raw[n:]
					if runes, ok := m.lookupBfchar(text, n); ok {
						r = append(r, runes...)
						continue Parse
					}
					if runes, ok := m.lookupBfrange(text, n); ok {
						r = append(r, runes...)
						continue Parse
					}
					r = append(r, noRune)
					continue Parse
				}
			}
		}
		if DebugOn {
			println("no code space found")
		}
		r = append(r, noRune)
		raw = raw[1:]
	}
	return string(r)
}

// cmapInterp holds mutable state for the readCmap Interpret callback.
type cmapInterp struct {
	n  int
	m  cmap
	ok bool
}

func (s *cmapInterp) handleEndCodespace(stk *Stack) {
	if s.n < 0 {
		if DebugOn {
			println("missing begincodespacerange")
		}
		s.ok = false
		return
	}
	for i := 0; i < s.n; i++ {
		hi, lo := stk.Pop().RawString(), stk.Pop().RawString()
		if len(lo) == 0 || len(lo) != len(hi) {
			if DebugOn {
				println("bad codespace range")
			}
			s.ok = false
			return
		}
		s.m.space[len(lo)-1] = append(s.m.space[len(lo)-1], byteRange{lo, hi})
	}
	s.n = -1
}

func (s *cmapInterp) handleEndBfchar(stk *Stack) {
	if s.n < 0 {
		panic("missing beginbfchar")
	}
	for i := 0; i < s.n; i++ {
		repl, orig := stk.Pop().RawString(), stk.Pop().RawString()
		s.m.bfchar = append(s.m.bfchar, bfchar{orig, repl})
	}
}

func (s *cmapInterp) handleEndBfrange(stk *Stack) {
	if s.n < 0 {
		panic("missing beginbfrange")
	}
	for i := 0; i < s.n; i++ {
		dst, srcHi, srcLo := stk.Pop(), stk.Pop().RawString(), stk.Pop().RawString()
		s.m.bfrange = append(s.m.bfrange, bfrange{srcLo, srcHi, dst})
	}
}

func (s *cmapInterp) interpretCmap(stk *Stack, op string) {
	if !s.ok {
		return
	}
	switch op {
	case "findresource":
		stk.Pop()
		stk.Pop()
		stk.Push(newDict())
	case "begincmap":
		stk.Push(newDict())
	case "endcmap":
		stk.Pop()
	case "begincodespacerange":
		s.n = int(stk.Pop().Int64())
	case "endcodespacerange":
		s.handleEndCodespace(stk)
	case "beginbfchar":
		s.n = int(stk.Pop().Int64())
	case "endbfchar":
		s.handleEndBfchar(stk)
	case "beginbfrange":
		s.n = int(stk.Pop().Int64())
	case "endbfrange":
		s.handleEndBfrange(stk)
	case "defineresource":
		stk.Pop().Name()
		value := stk.Pop()
		stk.Pop().Name()
		stk.Push(value)
	default:
		if DebugOn {
			println("interp\t", op)
		}
	}
}

func readCmap(toUnicode Value) *cmap {
	s := &cmapInterp{n: -1, ok: true}
	Interpret(toUnicode, s.interpretCmap)
	if !s.ok {
		return nil
	}
	return &s.m
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

// A Text represents a single piece of text drawn on a page.
type Text struct {
	Font     string  // the font used
	FontSize float64 // the font size, in points (1/72 of an inch)
	X        float64 // the X coordinate, in points, increasing left to right
	Y        float64 // the Y coordinate, in points, increasing bottom to top
	W        float64 // the width of the text, in points
	S        string  // the actual UTF-8 text
}

// A Rect represents a rectangle.
type Rect struct {
	Min, Max Point
}

// A Point represents an X, Y pair.
type Point struct {
	X float64
	Y float64
}

// Content describes the basic content on a page: the text and any drawn rectangles.
type Content struct {
	Text []Text
	Rect []Rect
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
type plainTextState struct {
	enc   TextEncoding
	fonts map[string]*Font
	buf   bytes.Buffer
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
	s := &plainTextState{enc: &nopEncoder{}, fonts: fonts}
	Interpret(p.V.Key("Contents"), s.interpretPlain)
	return s.buf.String(), nil
}

// Column represents the contents of a column
type Column struct {
	Position int64
	Content  TextVertical
}

// Columns is a list of column
type Columns []*Column

// GetTextByColumn returns the page's all text grouped by column
func (p Page) GetTextByColumn() (Columns, error) {
	result := Columns{}
	var err error

	defer func() {
		if r := recover(); r != nil {
			result = Columns{}
			err = errors.New(fmt.Sprint(r))
		}
	}()

	showText := func(enc TextEncoding, currentX, currentY float64, s string) {
		var textBuilder bytes.Buffer

		for _, ch := range enc.Decode(s) {
			_, err := textBuilder.WriteRune(ch)
			if err != nil {
				panic(err)
			}
		}
		text := Text{
			S: textBuilder.String(),
			X: currentX,
			Y: currentY,
		}

		var currentColumn *Column
		columnFound := false
		for _, column := range result {
			if int64(currentX) == column.Position {
				currentColumn = column
				columnFound = true
				break
			}
		}

		if !columnFound {
			currentColumn = &Column{
				Position: int64(currentX),
				Content:  TextVertical{},
			}
			result = append(result, currentColumn)
		}

		currentColumn.Content = append(currentColumn.Content, text)
	}

	p.walkTextBlocks(showText)

	for _, column := range result {
		sort.Stable(column.Content)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Position < result[j].Position
	})

	return result, err
}

// Row represents the contents of a row
type Row struct {
	Position int64
	Content  TextHorizontal
}

// Rows is a list of rows
type Rows []*Row

// GetTextByRow returns the page's all text grouped by rows
func (p Page) GetTextByRow() (Rows, error) {
	result := Rows{}
	var err error

	defer func() {
		if r := recover(); r != nil {
			result = Rows{}
			err = errors.New(fmt.Sprint(r))
		}
	}()

	showText := func(enc TextEncoding, currentX, currentY float64, s string) {
		var textBuilder bytes.Buffer
		for _, ch := range enc.Decode(s) {
			_, err := textBuilder.WriteRune(ch)
			if err != nil {
				panic(err)
			}
		}

		// if DebugOn {
		// 	fmt.Println(textBuilder.String())
		// }

		text := Text{
			S: textBuilder.String(),
			X: currentX,
			Y: currentY,
		}

		var currentRow *Row
		rowFound := false
		for _, row := range result {
			if int64(currentY) == row.Position {
				currentRow = row
				rowFound = true
				break
			}
		}

		if !rowFound {
			currentRow = &Row{
				Position: int64(currentY),
				Content:  TextHorizontal{},
			}
			result = append(result, currentRow)
		}

		currentRow.Content = append(currentRow.Content, text)
	}

	p.walkTextBlocks(showText)

	for _, row := range result {
		sort.Stable(row.Content)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Position > result[j].Position
	})

	return result, err
}

// walkState holds mutable state for the walkTextBlocks Interpret callback.
type walkState struct {
	enc    TextEncoding
	x, y   float64
	tl     float64
	fonts  map[string]*Font
	walker func(TextEncoding, float64, float64, string)
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
	s := &walkState{enc: &nopEncoder{}, fonts: fonts, walker: walker}
	Interpret(p.V.Key("Contents"), s.interpretWalk)
}

// contentState holds the mutable interpreter state for the Content() operator loop.
type contentState struct {
	g      gstate
	enc    TextEncoding
	text   []Text
	rect   []Rect
	gstack []gstate
	p      Page
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
	s.g.Tf = s.p.Font(f)
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
	}
}

// Content returns the page's content.
func (p Page) Content() Content {
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return Content{}
	}
	s := &contentState{
		g:   gstate{Th: 1, CTM: ident},
		enc: &nopEncoder{},
		p:   p,
	}
	Interpret(p.V.Key("Contents"), s.interpret)
	return Content{s.text, s.rect}
}

// TextVertical implements sort.Interface for sorting
// a slice of Text values in vertical order, top to bottom,
// and then left to right within a line.
type TextVertical []Text

func (x TextVertical) Len() int      { return len(x) }
func (x TextVertical) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x TextVertical) Less(i, j int) bool {
	if x[i].Y != x[j].Y {
		return x[i].Y > x[j].Y
	}
	return x[i].X < x[j].X
}

// TextHorizontal implements sort.Interface for sorting
// a slice of Text values in horizontal order, left to right,
// and then top to bottom within a column.
type TextHorizontal []Text

func (x TextHorizontal) Len() int      { return len(x) }
func (x TextHorizontal) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x TextHorizontal) Less(i, j int) bool {
	if x[i].X != x[j].X {
		return x[i].X < x[j].X
	}
	return x[i].Y > x[j].Y
}

// An Outline is a tree describing the outline (also known as the table of contents)
// of a document.
type Outline struct {
	Title string    // title for this element
	Page  int       // 1-based page number; 0 if destination cannot be resolved
	Child []Outline // child elements
}

// Outline returns the document outline.
// The Outline returned is the root of the outline tree and typically has no Title itself.
// That is, the children of the returned root are the top-level entries in the outline.
func (r *Reader) Outline() Outline {
	pages := r.buildPageMap()
	return buildOutline(r.Trailer().Key("Root").Key("Outlines"), pages)
}

func (r *Reader) buildPageMap() map[uint32]int {
	m := make(map[uint32]int)
	n := r.NumPage()
	for i := 1; i <= n; i++ {
		p := r.Page(i)
		if !p.V.IsNull() {
			m[p.V.ptr.id] = i
		}
	}
	return m
}

func buildOutline(entry Value, pages map[uint32]int) Outline {
	var x Outline
	x.Title = entry.Key("Title").Text()
	x.Page = resolveOutlineDest(entry, pages)
	for child := entry.Key("First"); child.Kind() == Dict; child = child.Key("Next") {
		x.Child = append(x.Child, buildOutline(child, pages))
	}
	return x
}

func resolveOutlineDest(entry Value, pages map[uint32]int) int {
	dest := entry.Key("Dest")
	if dest.Kind() == Array {
		return pageFromDestArray(dest, pages)
	}
	action := entry.Key("A")
	if action.Key("S").Name() == "GoTo" {
		d := action.Key("D")
		if d.Kind() == Array {
			return pageFromDestArray(d, pages)
		}
	}
	return 0
}

func pageFromDestArray(dest Value, pages map[uint32]int) int {
	if dest.Len() == 0 {
		return 0
	}
	pageVal := dest.Index(0)
	if n, ok := pages[pageVal.ptr.id]; ok {
		return n
	}
	return 0
}
