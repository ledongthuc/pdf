// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import "fmt"

// dictEncoder handles fonts with Encoding dictionaries containing
// BaseEncoding and/or Differences arrays per PDF spec section 9.6.6.
type dictEncoder struct {
	table [256]rune
}

func newDictEncoder(enc Value) *dictEncoder {
	e := &dictEncoder{}
	copy(e.table[:], baseEncodingTable(enc.Key("BaseEncoding"))[:])
	applyDifferences(&e.table, enc.Key("Differences"))
	return e
}

// baseEncodingTable returns the standard 256-rune table for the named base encoding.
func baseEncodingTable(baseEnc Value) *[256]rune {
	switch baseEnc.Name() {
	case "WinAnsiEncoding":
		return &winAnsiEncoding
	case "MacRomanEncoding":
		return &macRomanEncoding
	default:
		return &pdfDocEncoding
	}
}

// applyDifferences patches table with the name-to-code mappings from a PDF Differences array.
func applyDifferences(table *[256]rune, diff Value) {
	if diff.Kind() != Array {
		return
	}
	code := -1
	for j := 0; j < diff.Len(); j++ {
		x := diff.Index(j)
		if x.Kind() == Integer {
			code = int(x.Int64())
			continue
		}
		if x.Kind() == Name && code >= 0 && code < 256 {
			if r := nameToRune[x.Name()]; r != 0 {
				table[code] = r
			}
			code++
		}
	}
}

func (e *dictEncoder) Decode(raw string) (text string) {
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
	for _, entry := range m.bfrange {
		if len(entry.lo) == n && entry.lo <= text && text <= entry.hi {
			return decodeBfrange(entry, text)
		}
	}
	return nil, false
}

// decodeBfrange maps text against a matched bfrange entry, handling String and Array destinations.
func decodeBfrange(entry bfrange, text string) ([]rune, bool) {
	if entry.dst.Kind() == String {
		s := entry.dst.RawString()
		if entry.lo != text {
			b := []byte(s)
			b[len(b)-1] += text[len(text)-1] - entry.lo[len(entry.lo)-1]
			s = string(b)
		}
		return []rune(utf16Decode(s)), true
	}
	if entry.dst.Kind() == Array {
		idx := text[len(text)-1] - entry.lo[len(entry.lo)-1]
		v := entry.dst.Index(int(idx))
		if v.Kind() == String {
			return []rune(utf16Decode(v.RawString())), true
		}
		if DebugOn {
			fmt.Printf("array %v\n", entry.dst)
		}
	} else {
		if DebugOn {
			fmt.Printf("unknown dst %v\n", entry.dst)
		}
	}
	return []rune{noRune}, true
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
