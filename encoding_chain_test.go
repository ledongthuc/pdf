// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"testing"
)

func TestResolveGlyphName(t *testing.T) {
	tests := []struct {
		name string
		want rune
	}{
		// Standard glyph names from nameToRune
		{"space", ' '},
		{"exclam", '!'},
		{"period", '.'},
		{"comma", ','},
		{"colon", ':'},
		{"semicolon", ';'},
		{"hyphen", '-'},
		{"endash", '–'},
		{"emdash", '—'},
		{"quotesingle", '\''},
		{"quotedbl", '"'},
		{"parenleft", '('},
		{"parenright", ')'},
		{"bracketleft", '['},
		{"bracketright", ']'},
		{"braceleft", '{'},
		{"braceright", '}'},
		{"slash", '/'},
		{"backslash", '\\'},
		{"at", '@'},
		{"numbersign", '#'},
		{"dollar", '$'},
		{"percent", '%'},
		{"ampersand", '&'},
		{"asterisk", '*'},
		{"plus", '+'},
		{"equal", '='},
		{"less", '<'},
		{"greater", '>'},
		{"underscore", '_'},

		// Digit names
		{"zero", '0'},
		{"one", '1'},
		{"two", '2'},
		{"three", '3'},
		{"four", '4'},
		{"five", '5'},
		{"six", '6'},
		{"seven", '7'},
		{"eight", '8'},
		{"nine", '9'},

		// uniXXXX pattern (4 hex digits after "uni")
		{"uni0041", 'A'},
		{"uni0061", 'a'},
		{"uni00E9", 0x00E9},   // é
		{"uni2019", 0x2019},   // right single quote
		{"uni201C", 0x201C},   // left double quote
		{"uni201D", 0x201D},   // right double quote

		// uXXXX pattern (4 hex digits after "u")
		{"u0041", 'A'},
		{"u0061", 'a'},
		{"u00E9", 0x00E9}, // é

		// uXXXXXX pattern (6 hex digits after "u")
		{"u01F600", 0x1F600}, // grinning face emoji

		// aXXX pattern (decimal after "a")
		{"a65", 'A'},
		{"a97", 'a'},

		// Single character glyph names
		{"A", 'A'},
		{"Z", 'Z'},
		{"a", 'a'},
		{"z", 'z'},

		// Unknown names should return 0
		{"unknownglyph", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGlyphName(tt.name)
			if got != tt.want {
				t.Errorf("resolveGlyphName(%q) = %q (U+%04X), want %q (U+%04X)",
					tt.name, string(got), got, string(tt.want), tt.want)
			}
		})
	}
}

func TestFontEncodingChain_isValidDecode(t *testing.T) {
	chain := &FontEncodingChain{}

	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty string", "", false},
		{"all valid ASCII", "Hello World", true},
		{"all valid Unicode", "Hello World", true},
		// Threshold is < 50% (strictly less than)
		{"under threshold", string([]rune{'H', 'e', 'l', 'l', 'o', noRune}), true},          // 1/6 = 16.7%
		{"at threshold", string([]rune{'H', noRune, 'l', 'l', 'o'}), true},                  // 1/5 = 20% (< 50%)
		{"just under 50%", string([]rune{noRune, noRune, 'l', 'l', 'o'}), true},              // 2/5 = 40% (< 50%)
		{"at 50%", string([]rune{noRune, noRune, noRune, 'l', 'l', 'o'}), false},             // 3/6 = 50% (not < 50%)
		{"over threshold", string([]rune{noRune, noRune, noRune, noRune, 'o'}), false},       // 4/5 = 80%
		{"all replacements", string([]rune{noRune, noRune, noRune}), false},
		{"single char valid", "A", true},
		{"single char invalid", string(noRune), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chain.isValidDecode(tt.text)
			if got != tt.want {
				t.Errorf("isValidDecode(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestFontEncodingChain_decodeSimple(t *testing.T) {
	tests := []struct {
		name         string
		baseEncoding *[256]rune
		differences  map[byte]rune
		input        string
		want         string
	}{
		{
			name:         "basic ASCII with pdfDocEncoding",
			baseEncoding: &pdfDocEncoding,
			differences:  nil,
			input:        "Hello",
			want:         "Hello",
		},
		{
			name:         "WinAnsi special chars",
			baseEncoding: &winAnsiEncoding,
			differences:  nil,
			input:        string([]byte{0x93, 0x94}), // smart quotes in WinAnsi
			want:         string([]rune{0x201C, 0x201D}), // left/right double quotes
		},
		{
			name:         "differences override base",
			baseEncoding: &pdfDocEncoding,
			differences:  map[byte]rune{0x41: 'X'}, // A -> X
			input:        "ABC",
			want:         "XBC",
		},
		{
			name:         "multiple differences",
			baseEncoding: &pdfDocEncoding,
			differences: map[byte]rune{
				0x41: '1', // A -> 1
				0x42: '2', // B -> 2
				0x43: '3', // C -> 3
			},
			input: "ABC",
			want:  "123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := &FontEncodingChain{
				baseEncoding: tt.baseEncoding,
				differences:  tt.differences,
			}
			if chain.differences == nil {
				chain.differences = make(map[byte]rune)
			}

			got := chain.decodeSimple(tt.input)
			if got != tt.want {
				t.Errorf("decodeSimple(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetPredefinedCMap(t *testing.T) {
	tests := []struct {
		name     string
		wantNil  bool
		wantDesc string
	}{
		{"Identity-H", false, "identity horizontal CMap"},
		{"Identity-V", false, "identity vertical CMap"},
		{"UniJIS-UTF16-H", true, "not implemented"},
		{"unknown", true, "unknown CMap"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPredefinedCMap(tt.name)
			if (got == nil) != tt.wantNil {
				t.Errorf("getPredefinedCMap(%q) nil = %v, want nil = %v (%s)",
					tt.name, got == nil, tt.wantNil, tt.wantDesc)
			}
		})
	}
}

func TestStandardEncoding(t *testing.T) {
	// Verify key code points in standardEncoding
	tests := []struct {
		code byte
		want rune
	}{
		{0x20, ' '},
		{0x21, '!'},
		{0x30, '0'},
		{0x41, 'A'},
		{0x61, 'a'},
		{0x7E, '~'},
		{0xA1, 0x00A1}, // inverted exclamation mark
		{0xBF, 0x00BF}, // inverted question mark
		{0xB1, 0x2013}, // endash
		{0xD0, 0x2014}, // emdash
	}

	for _, tt := range tests {
		t.Run(string(tt.want), func(t *testing.T) {
			got := standardEncoding[tt.code]
			if got != tt.want {
				t.Errorf("standardEncoding[0x%02X] = %q (U+%04X), want %q (U+%04X)",
					tt.code, string(got), got, string(tt.want), tt.want)
			}
		})
	}
}

func TestFontEncodingChain_Decode_Priority(t *testing.T) {
	// Test that ToUnicode CMap takes priority when present and valid

	// Create a chain with both ToUnicode and simple encoding
	chain := &FontEncodingChain{
		baseEncoding: &pdfDocEncoding,
		differences:  make(map[byte]rune),
	}

	// Without ToUnicode, should use simple encoding
	input := "ABC"
	got := chain.Decode(input)
	if got != "ABC" {
		t.Errorf("Decode without ToUnicode = %q, want %q", got, "ABC")
	}
}

func TestFontEncodingChain_decodeWithGlyphHeuristics(t *testing.T) {
	chain := &FontEncodingChain{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ASCII printable", "Hello", "Hello"},
		{"tab character", "\t", "\t"},
		{"newline", "\n", "\n"},
		{"carriage return", "\r", "\r"},
		{"mixed", "Hi\tthere\n", "Hi\tthere\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chain.decodeWithGlyphHeuristics(tt.input)
			if got != tt.want {
				t.Errorf("decodeWithGlyphHeuristics(%q) = %q, want %q",
					tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveGlyphNameMulti(t *testing.T) {
	tests := []struct {
		name string
		want []rune
	}{
		// Single-rune: standard names resolve via resolveGlyphName
		{"A", []rune{'A'}},
		{"space", []rune{' '}},
		{"onehalf", []rune{'½'}},
		{"onequarter", []rune{'¼'}},

		// Single-rune: suffix stripping finds standard name
		{"onehalf.alt", []rune{'½'}},

		// Multi-rune: ligature with suffix
		{"t_t.liga", []rune{'t', 't'}},
		{"f_i.liga", []rune{'f', 'i'}},
		{"f_l.liga", []rune{'f', 'l'}},
		{"f_f.liga", []rune{'f', 'f'}},

		// Multi-rune: ligature without suffix
		{"t_t", []rune{'t', 't'}},
		{"f_i", []rune{'f', 'i'}},
		{"f_f_l", []rune{'f', 'f', 'l'}},
		{"f_f_i", []rune{'f', 'f', 'i'}},

		// Triple ligature with suffix
		{"f_f_l.liga", []rune{'f', 'f', 'l'}},

		// Unknown names
		{"nonexistent", nil},
		{"", nil},

		// Partially resolvable ligature (one component unknown)
		{"t_unknownglyph", nil},

		// Edge cases: leading/trailing underscore, dot-only, leading dot
		{"_foo", nil},         // leading underscore → splits to ["", "foo"], "" resolves to 0
		{"fi_", nil},          // trailing underscore → splits to ["fi", ""], "" resolves to 0
		{".liga", nil},        // dot at position 0, i > 0 guard skips suffix strip
		{"t_.liga", nil},      // component "" after split can't resolve
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGlyphNameMulti(tt.name)
			if len(got) != len(tt.want) {
				t.Errorf("resolveGlyphNameMulti(%q) = %v, want %v", tt.name, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("resolveGlyphNameMulti(%q)[%d] = %q, want %q", tt.name, i, string(got[i]), string(tt.want[i]))
				}
			}
		})
	}
}

func TestDecodeSimpleWithLigatures(t *testing.T) {
	chain := &FontEncodingChain{
		baseEncoding:     &pdfDocEncoding,
		differences:      make(map[byte]rune),
		multiDifferences: map[byte][]rune{0x21: {'t', 't'}},
	}

	// Code 0x21 should produce "tt" via multiDifferences
	got := chain.decodeSimple(string([]byte{0x21}))
	if got != "tt" {
		t.Errorf("decodeSimple(0x21) = %q, want %q", got, "tt")
	}

	// Normal codes should still work
	got = chain.decodeSimple("Hello")
	if got != "Hello" {
		t.Errorf("decodeSimple(\"Hello\") = %q, want %q", got, "Hello")
	}

	// Mixed: normal + ligature + normal
	got = chain.decodeSimple(string([]byte{'L', 'e', 0x21, 'u', 'c', 'e'}))
	if got != "Lettuce" {
		t.Errorf("decodeSimple for Lettuce = %q, want %q", got, "Lettuce")
	}
}

func TestResolvePUAWithDifferences(t *testing.T) {
	chain := &FontEncodingChain{
		baseEncoding:     &pdfDocEncoding,
		differences:      map[byte]rune{0x22: '¼'},
		multiDifferences: map[byte][]rune{0x21: {'t', 't'}},
	}

	// PUA char U+E021 should be resolved to "tt" via multiDifferences
	got := chain.resolvePUAWithDifferences(string([]rune{0xE021}))
	if got != "tt" {
		t.Errorf("resolvePUAWithDifferences(U+E021) = %q, want %q", got, "tt")
	}

	// PUA char U+E022 should be resolved to '¼' via single-rune differences
	got = chain.resolvePUAWithDifferences(string([]rune{0xE022}))
	if got != "¼" {
		t.Errorf("resolvePUAWithDifferences(U+E022) = %q, want %q", got, "¼")
	}

	// Mixed: normal CMap result + PUA that needs resolution
	got = chain.resolvePUAWithDifferences(string([]rune{'L', 'e', 0xE021, 'u', 'c', 'e'}))
	if got != "Lettuce" {
		t.Errorf("resolvePUAWithDifferences for Lettuce = %q, want %q", got, "Lettuce")
	}

	// PUA with no Differences entry passes through unchanged
	got = chain.resolvePUAWithDifferences(string([]rune{0xE099}))
	if got != string([]rune{0xE099}) {
		t.Errorf("resolvePUAWithDifferences(unmapped PUA) should pass through, got %q", got)
	}
}

func TestDecodeEndToEndWithCMapAndDifferences(t *testing.T) {
	// Build a CMap that maps codes 0x41='A' and 0x42='B' but NOT 0x21.
	// Code 0x21 is in the codespace so it gets PUA. Differences maps
	// 0x21 → "tt" via multiDifferences. Decode() should return the
	// improved result with "tt" replacing the PUA char.
	testCmap := &cmap{
		space: [4][]byteRange{
			{{low: "\x00", high: "\xff"}}, // 1-byte codespace
			{}, {}, {},
		},
		bfchar: []bfchar{},
		bfrange: []bfrange{
			{lo: "\x41", hi: "\x42", dst: Value{data: string([]byte{0x00, 0x41})}},
		},
	}

	chain := &FontEncodingChain{
		toUnicodeCMap:    testCmap,
		baseEncoding:     &pdfDocEncoding,
		differences:      make(map[byte]rune),
		multiDifferences: map[byte][]rune{0x21: {'t', 't'}},
	}

	// Code 0x41 is in the CMap → should decode to 'A'
	got := chain.Decode("\x41")
	if got != "A" {
		t.Errorf("Decode(0x41) = %q, want %q", got, "A")
	}

	// Code 0x21 is NOT in CMap → PUA → resolved via Differences → "tt"
	got = chain.Decode("\x21")
	if got != "tt" {
		t.Errorf("Decode(0x21) = %q, want %q", got, "tt")
	}

	// Mixed: 'A' (CMap) + 'tt' (Differences) + 'B' (CMap)
	got = chain.Decode("\x41\x21\x42")
	if got != "AttB" {
		t.Errorf("Decode(0x41,0x21,0x42) = %q, want %q", got, "AttB")
	}
}

func TestDecodeReturnsValidCMapEvenWithResidualPUA(t *testing.T) {
	// CMap maps 0x41→'A' but not 0x99. Differences has no entry for 0x99
	// either. Decode() should still return the CMap result (with PUA for
	// 0x99) rather than falling through to Layer 3.
	testCmap := &cmap{
		space: [4][]byteRange{
			{{low: "\x00", high: "\xff"}},
			{}, {}, {},
		},
		bfrange: []bfrange{
			{lo: "\x41", hi: "\x41", dst: Value{data: string([]byte{0x00, 0x41})}},
		},
	}

	chain := &FontEncodingChain{
		toUnicodeCMap:    testCmap,
		baseEncoding:     &pdfDocEncoding,
		differences:      make(map[byte]rune),
		multiDifferences: make(map[byte][]rune),
	}

	// Should get 'A' + PUA(0x99), not fall through to Layer 3
	got := chain.Decode("\x41\x99")
	if !chain.containsPUA(got) {
		t.Errorf("expected PUA in result for unmapped code with no Differences, got %q", got)
	}
	// Should still contain the 'A' from CMap
	if len([]rune(got)) != 2 || []rune(got)[0] != 'A' {
		t.Errorf("Decode should preserve CMap result, got %q", got)
	}
}

func TestContainsPUA(t *testing.T) {
	chain := &FontEncodingChain{}

	tests := []struct {
		name string
		text string
		want bool
	}{
		{"no PUA", "Hello World", false},
		{"with PUA", string([]rune{'H', 0xE021, 'o'}), true},
		{"only PUA", string([]rune{0xE000, 0xE0FF}), true},
		{"empty", "", false},
		{"above PUA range", string([]rune{0xE100}), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chain.containsPUA(tt.text)
			if got != tt.want {
				t.Errorf("containsPUA(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// Benchmark the encoding chain decode performance
func BenchmarkFontEncodingChain_Decode(b *testing.B) {
	chain := &FontEncodingChain{
		baseEncoding:     &winAnsiEncoding,
		differences:      make(map[byte]rune),
		multiDifferences: make(map[byte][]rune),
	}

	input := "The quick brown fox jumps over the lazy dog. 0123456789"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = chain.Decode(input)
	}
}

func BenchmarkResolveGlyphName(b *testing.B) {
	names := []string{"space", "uni0041", "u0061", "a65", "A", "unknown"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, name := range names {
			_ = resolveGlyphName(name)
		}
	}
}
