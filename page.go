// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"bytes"
	"context"
	"io"
	"iter"
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

// GetPlainText returns all the text in the PDF file.
// The context is checked once per page; cancel it to interrupt processing.
func (r *Reader) GetPlainText(ctx context.Context) (reader io.Reader, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pages := r.NumPage()
	var buf bytes.Buffer
	for i := 1; i <= pages; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		p := r.Page(i)
		text, err := p.GetPlainText(nil)
		if err != nil {
			return &bytes.Buffer{}, err
		}
		buf.WriteString(text)
	}
	return &buf, nil
}

// GetStyledTexts returns list all sentences in an array, that are included styles.
// The context is checked once per page; cancel it to interrupt processing.
func (r *Reader) GetStyledTexts(ctx context.Context) (sentences []Text, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	totalPage := r.NumPage()
	for pageIndex := 1; pageIndex <= totalPage; pageIndex++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
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
