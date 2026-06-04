// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

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
