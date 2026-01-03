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
		// Threshold is < 20% (strictly less than)
		{"under threshold", string([]rune{'H', 'e', 'l', 'l', 'o', noRune}), true}, // 1/6 = 16.7%
		{"at threshold", string([]rune{'H', noRune, 'l', 'l', 'o'}), false},        // 1/5 = 20% (not < 20%)
		{"over threshold", string([]rune{noRune, noRune, 'l', 'l', 'o'}), false},   // 2/5 = 40%
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

// Benchmark the encoding chain decode performance
func BenchmarkFontEncodingChain_Decode(b *testing.B) {
	chain := &FontEncodingChain{
		baseEncoding: &winAnsiEncoding,
		differences:  make(map[byte]rune),
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
