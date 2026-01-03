// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"strconv"
	"strings"
	"unicode"
)

// FontEncodingChain implements a multi-layer font decoding strategy.
// It tries each layer in order until one produces valid output:
//  1. ToUnicode CMap (highest confidence)
//  2. CIDFont chain: CharCode → CID → Unicode
//  3. Encoding + Differences
//  4. Glyph name resolution (Adobe Glyph List)
//  5. Fallback heuristics
type FontEncodingChain struct {
	// Layer 1: ToUnicode CMap (if present)
	toUnicodeCMap *cmap

	// Layer 2: For CIDFonts (Type0 fonts)
	isCIDFont       bool
	codeToCIDMap    *cmap  // Maps char code to CID
	cidToUnicodeMap *cmap  // Maps CID to Unicode
	cidSystemInfo   string // Registry-Ordering-Supplement

	// Layer 3: Simple font encoding
	baseEncoding *[256]rune
	differences  map[byte]rune

	// Layer 4: Font info for fallback
	fontName string
}

// NewFontEncodingChain creates an encoding chain for the given font.
func NewFontEncodingChain(f Font) *FontEncodingChain {
	chain := &FontEncodingChain{
		differences: make(map[byte]rune),
		fontName:    f.BaseFont(),
	}

	// Check font type - Type0 fonts are CIDFonts
	subtype := f.V.Key("Subtype").Name()
	if subtype == "Type0" {
		chain.isCIDFont = true
		chain.buildCIDFontChain(f)
	}

	// Layer 1: ToUnicode CMap (always try first if present)
	toUnicode := f.V.Key("ToUnicode")
	if toUnicode.Kind() == Stream {
		chain.toUnicodeCMap = readCmap(toUnicode)
	}

	// Layer 3: Build simple encoding table
	chain.buildSimpleEncoding(f)

	return chain
}

// buildCIDFontChain sets up CIDFont decoding for Type0 fonts.
func (chain *FontEncodingChain) buildCIDFontChain(f Font) {
	// Get DescendantFonts array (required for Type0)
	descendants := f.V.Key("DescendantFonts")
	if descendants.Kind() != Array || descendants.Len() == 0 {
		return
	}

	// First descendant contains the CIDFont
	cidFont := descendants.Index(0)
	if cidFont.Kind() != Dict {
		return
	}

	// Get CIDSystemInfo
	cidSysInfo := cidFont.Key("CIDSystemInfo")
	if cidSysInfo.Kind() == Dict {
		registry := cidSysInfo.Key("Registry").Text()
		ordering := cidSysInfo.Key("Ordering").Text()
		supplement := cidSysInfo.Key("Supplement").Int64()
		chain.cidSystemInfo = registry + "-" + ordering + "-" + strconv.FormatInt(supplement, 10)
	}

	// Get encoding for code-to-CID mapping
	encoding := f.V.Key("Encoding")
	if encoding.Kind() == Stream {
		chain.codeToCIDMap = readCmap(encoding)
	} else if encoding.Kind() == Name {
		// Predefined CMap name (e.g., "Identity-H", "UniJIS-UTF16-H")
		cmapName := encoding.Name()
		chain.codeToCIDMap = getPredefinedCMap(cmapName)
	}

	// Try to get CIDToGIDMap for TrueType-based CIDFonts
	cidToGID := cidFont.Key("CIDToGIDMap")
	if cidToGID.Kind() == Stream {
		// This maps CID to glyph ID - useful for embedded fonts
		// For now, we treat identity mapping
	}
}

// buildSimpleEncoding builds the encoding table for simple fonts.
func (chain *FontEncodingChain) buildSimpleEncoding(f Font) {
	enc := f.V.Key("Encoding")

	// Default to PDFDocEncoding
	chain.baseEncoding = &pdfDocEncoding

	switch enc.Kind() {
	case Name:
		switch enc.Name() {
		case "WinAnsiEncoding":
			chain.baseEncoding = &winAnsiEncoding
		case "MacRomanEncoding":
			chain.baseEncoding = &macRomanEncoding
		case "StandardEncoding":
			chain.baseEncoding = &standardEncoding
		case "MacExpertEncoding":
			chain.baseEncoding = &macExpertEncoding
		}
	case Dict:
		// Handle BaseEncoding
		baseEnc := enc.Key("BaseEncoding")
		switch baseEnc.Name() {
		case "WinAnsiEncoding":
			chain.baseEncoding = &winAnsiEncoding
		case "MacRomanEncoding":
			chain.baseEncoding = &macRomanEncoding
		case "StandardEncoding":
			chain.baseEncoding = &standardEncoding
		}

		// Apply Differences array
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
					glyphName := x.Name()
					if r := resolveGlyphName(glyphName); r != 0 {
						chain.differences[byte(code)] = r
					}
					code++
				}
			}
		}
	}
}

// Decode converts raw PDF character codes to Unicode text.
func (chain *FontEncodingChain) Decode(raw string) string {
	if len(raw) == 0 {
		return ""
	}

	// Layer 1: Try ToUnicode CMap first (most reliable)
	if chain.toUnicodeCMap != nil {
		result := chain.toUnicodeCMap.Decode(raw)
		if chain.isValidDecode(result) {
			return result
		}
	}

	// Layer 2: For CIDFonts, try CID chain
	if chain.isCIDFont && chain.codeToCIDMap != nil {
		result := chain.decodeCIDFont(raw)
		if chain.isValidDecode(result) {
			return result
		}
	}

	// Layer 3: Simple encoding with differences
	result := chain.decodeSimple(raw)
	if chain.isValidDecode(result) {
		return result
	}

	// Layer 4: Try glyph name heuristics
	result = chain.decodeWithGlyphHeuristics(raw)
	if chain.isValidDecode(result) {
		return result
	}

	// Layer 5: Fallback - return simple decode even if not perfect
	return chain.decodeSimple(raw)
}

// decodeCIDFont decodes using the CIDFont chain.
func (chain *FontEncodingChain) decodeCIDFont(raw string) string {
	if chain.codeToCIDMap == nil {
		return ""
	}

	// For CIDFonts, character codes may be 1 or 2 bytes
	// The CMap defines the code space
	result := chain.codeToCIDMap.Decode(raw)

	// If we have a CID-to-Unicode map, apply it
	if chain.cidToUnicodeMap != nil && result != "" {
		// The result from codeToCIDMap might be CIDs, need to map to Unicode
		// This is complex - for now, return the result if it looks valid
	}

	return result
}

// decodeSimple uses the simple encoding table with differences.
func (chain *FontEncodingChain) decodeSimple(raw string) string {
	if chain.baseEncoding == nil {
		return raw
	}

	var result strings.Builder
	result.Grow(len(raw))

	for i := 0; i < len(raw); i++ {
		code := raw[i]

		// Check differences first
		if r, ok := chain.differences[code]; ok {
			result.WriteRune(r)
			continue
		}

		// Fall back to base encoding
		r := chain.baseEncoding[code]
		if r == 0 {
			// Preserve raw byte in Private Use Area (U+E000-U+E0FF)
			// This allows post-processing to recover the original byte:
			// originalByte = rune - 0xE000
			// This is better than U+FFFD which loses the information entirely.
			r = rune(0xE000 + int(code))
		}
		result.WriteRune(r)
	}

	return result.String()
}

// decodeWithGlyphHeuristics tries to decode using glyph name patterns.
func (chain *FontEncodingChain) decodeWithGlyphHeuristics(raw string) string {
	// This is a last resort - try common patterns
	var result strings.Builder
	result.Grow(len(raw))

	for i := 0; i < len(raw); i++ {
		code := raw[i]

		// Try various heuristics
		r := rune(0)

		// Heuristic 1: Code point might be direct Unicode in BMP
		if code >= 0x20 && code < 0x7F {
			r = rune(code) // ASCII range
		}

		// Heuristic 2: Check if it's a common control character
		if r == 0 && code < 0x20 {
			switch code {
			case 0x09:
				r = '\t'
			case 0x0A:
				r = '\n'
			case 0x0D:
				r = '\r'
			}
		}

		if r == 0 {
			// Preserve raw byte in PUA for post-processing
			r = rune(0xE000 + int(code))
		}
		result.WriteRune(r)
	}

	return result.String()
}

// isValidDecode checks if the decoded text looks valid (not too many replacement chars).
func (chain *FontEncodingChain) isValidDecode(text string) bool {
	if len(text) == 0 {
		return false
	}

	replacementCount := 0
	totalCount := 0

	for _, r := range text {
		totalCount++
		if r == noRune || r == unicode.ReplacementChar {
			replacementCount++
		}
		// Note: PUA characters (U+E000-U+E0FF) are not counted as failures
		// because they preserve the original byte value for post-processing
	}

	// Accept if less than 50% replacement characters (raised from 20%)
	// This allows more text through for post-processing, which can apply
	// custom decodings (e.g., shifted encodings) to PUA-preserved bytes.
	return totalCount > 0 && float64(replacementCount)/float64(totalCount) < 0.5
}

// resolveGlyphName converts a glyph name to its Unicode code point.
func resolveGlyphName(name string) rune {
	// First, check the standard name-to-rune mapping
	if r, ok := nameToRune[name]; ok {
		return r
	}

	// Handle uniXXXX pattern (Unicode code point)
	if strings.HasPrefix(name, "uni") && len(name) == 7 {
		if code, err := strconv.ParseInt(name[3:], 16, 32); err == nil {
			return rune(code)
		}
	}

	// Handle uXXXX or uXXXXXX pattern
	if strings.HasPrefix(name, "u") && (len(name) == 5 || len(name) == 7) {
		if code, err := strconv.ParseInt(name[1:], 16, 32); err == nil {
			return rune(code)
		}
	}

	// Handle gXXXX pattern (glyph ID - less reliable)
	// Skip this as it requires font-specific mapping

	// Handle aXXX pattern (some fonts use decimal)
	if strings.HasPrefix(name, "a") && len(name) >= 2 {
		if code, err := strconv.ParseInt(name[1:], 10, 32); err == nil && code > 0 && code < 0x10000 {
			return rune(code)
		}
	}

	// Single character names map to themselves
	if len(name) == 1 {
		return rune(name[0])
	}

	// Common single-letter glyph names
	singleLetterGlyphs := map[string]rune{
		"A": 'A', "B": 'B', "C": 'C', "D": 'D', "E": 'E', "F": 'F', "G": 'G',
		"H": 'H', "I": 'I', "J": 'J', "K": 'K', "L": 'L', "M": 'M', "N": 'N',
		"O": 'O', "P": 'P', "Q": 'Q', "R": 'R', "S": 'S', "T": 'T', "U": 'U',
		"V": 'V', "W": 'W', "X": 'X', "Y": 'Y', "Z": 'Z',
		"a": 'a', "b": 'b', "c": 'c', "d": 'd', "e": 'e', "f": 'f', "g": 'g',
		"h": 'h', "i": 'i', "j": 'j', "k": 'k', "l": 'l', "m": 'm', "n": 'n',
		"o": 'o', "p": 'p', "q": 'q', "r": 'r', "s": 's', "t": 't', "u": 'u',
		"v": 'v', "w": 'w', "x": 'x', "y": 'y', "z": 'z',
		"zero": '0', "one": '1', "two": '2', "three": '3', "four": '4',
		"five": '5', "six": '6', "seven": '7', "eight": '8', "nine": '9',
		"period": '.', "comma": ',', "colon": ':', "semicolon": ';',
		"hyphen": '-', "endash": '–', "emdash": '—',
		"space": ' ', "exclam": '!', "question": '?',
		"quotesingle": '\'', "quotedbl": '"',
		"parenleft": '(', "parenright": ')',
		"bracketleft": '[', "bracketright": ']',
		"braceleft": '{', "braceright": '}',
		"slash": '/', "backslash": '\\',
		"at": '@', "numbersign": '#', "dollar": '$', "percent": '%',
		"ampersand": '&', "asterisk": '*', "plus": '+', "equal": '=',
		"less": '<', "greater": '>', "underscore": '_', "asciicircum": '^',
		"asciitilde": '~', "bar": '|', "grave": '`',
	}

	if r, ok := singleLetterGlyphs[name]; ok {
		return r
	}

	return 0
}

// getPredefinedCMap returns a predefined CMap by name.
// This handles common CMap names like "Identity-H", "Identity-V", etc.
func getPredefinedCMap(name string) *cmap {
	switch name {
	case "Identity-H", "Identity-V":
		// Identity mapping: 2-byte codes map directly to CIDs
		return &cmap{
			space: [4][]byteRange{
				{}, // 1-byte: none
				{{low: "\x00\x00", high: "\xFF\xFF"}}, // 2-byte: all
				{}, // 3-byte: none
				{}, // 4-byte: none
			},
			// Identity means code == CID, so we let bfrange handle it
		}
	default:
		// For other CMaps (UniJIS, etc.), we'd need to load from resources
		// For now, return nil to fall through to other layers
		return nil
	}
}

// StandardEncoding as defined in PDF spec Appendix D
var standardEncoding = [256]rune{
	0x00: 0, 0x01: 0, 0x02: 0, 0x03: 0, 0x04: 0, 0x05: 0, 0x06: 0, 0x07: 0,
	0x08: 0, 0x09: 0, 0x0A: 0, 0x0B: 0, 0x0C: 0, 0x0D: 0, 0x0E: 0, 0x0F: 0,
	0x10: 0, 0x11: 0, 0x12: 0, 0x13: 0, 0x14: 0, 0x15: 0, 0x16: 0, 0x17: 0,
	0x18: 0, 0x19: 0, 0x1A: 0, 0x1B: 0, 0x1C: 0, 0x1D: 0, 0x1E: 0, 0x1F: 0,
	0x20: ' ', 0x21: '!', 0x22: '"', 0x23: '#', 0x24: '$', 0x25: '%', 0x26: '&', 0x27: '\'',
	0x28: '(', 0x29: ')', 0x2A: '*', 0x2B: '+', 0x2C: ',', 0x2D: '-', 0x2E: '.', 0x2F: '/',
	0x30: '0', 0x31: '1', 0x32: '2', 0x33: '3', 0x34: '4', 0x35: '5', 0x36: '6', 0x37: '7',
	0x38: '8', 0x39: '9', 0x3A: ':', 0x3B: ';', 0x3C: '<', 0x3D: '=', 0x3E: '>', 0x3F: '?',
	0x40: '@', 0x41: 'A', 0x42: 'B', 0x43: 'C', 0x44: 'D', 0x45: 'E', 0x46: 'F', 0x47: 'G',
	0x48: 'H', 0x49: 'I', 0x4A: 'J', 0x4B: 'K', 0x4C: 'L', 0x4D: 'M', 0x4E: 'N', 0x4F: 'O',
	0x50: 'P', 0x51: 'Q', 0x52: 'R', 0x53: 'S', 0x54: 'T', 0x55: 'U', 0x56: 'V', 0x57: 'W',
	0x58: 'X', 0x59: 'Y', 0x5A: 'Z', 0x5B: '[', 0x5C: '\\', 0x5D: ']', 0x5E: '^', 0x5F: '_',
	0x60: '`', 0x61: 'a', 0x62: 'b', 0x63: 'c', 0x64: 'd', 0x65: 'e', 0x66: 'f', 0x67: 'g',
	0x68: 'h', 0x69: 'i', 0x6A: 'j', 0x6B: 'k', 0x6C: 'l', 0x6D: 'm', 0x6E: 'n', 0x6F: 'o',
	0x70: 'p', 0x71: 'q', 0x72: 'r', 0x73: 's', 0x74: 't', 0x75: 'u', 0x76: 'v', 0x77: 'w',
	0x78: 'x', 0x79: 'y', 0x7A: 'z', 0x7B: '{', 0x7C: '|', 0x7D: '}', 0x7E: '~', 0x7F: 0,
	// 0x80-0x9F: undefined
	0xA1: '¡', 0xA2: '¢', 0xA3: '£', 0xA4: '⁄', 0xA5: '¥', 0xA6: 'ƒ', 0xA7: '§',
	0xA8: '¤', 0xA9: '\'', 0xAA: '"', 0xAB: '«', 0xAC: '‹', 0xAD: '›', 0xAE: 0xFB01, 0xAF: 0xFB02, // fi, fl ligatures
	0xB1: '–', 0xB2: '†', 0xB3: '‡', 0xB4: '·', 0xB6: '¶', 0xB7: '•',
	0xB8: '‚', 0xB9: '„', 0xBA: '"', 0xBB: '»', 0xBC: '…', 0xBD: '‰',
	0xBF: '¿',
	0xC1: '`', 0xC2: '´', 0xC3: 'ˆ', 0xC4: '˜', 0xC5: '¯', 0xC6: '˘', 0xC7: '˙',
	0xC8: '¨', 0xCA: '˚', 0xCB: '¸', 0xCD: '˝', 0xCE: '˛', 0xCF: 'ˇ',
	0xD0: '—',
	0xE1: 'Æ', 0xE3: 'ª', 0xE8: 'Ł', 0xE9: 'Ø', 0xEA: 'Œ', 0xEB: 'º',
	0xF1: 'æ', 0xF5: 'ı', 0xF8: 'ł', 0xF9: 'ø', 0xFA: 'œ', 0xFB: 'ß',
}

// MacExpertEncoding for expert/small caps fonts
var macExpertEncoding = [256]rune{
	// Mostly maps to standard positions but with expert glyphs
	// For now, fall back to standard encoding behavior
	0x20: ' ',
	// ... (abbreviated - would need full table for production)
}

func init() {
	// Initialize macExpertEncoding to match pdfDocEncoding as fallback
	for i := range macExpertEncoding {
		if macExpertEncoding[i] == 0 && pdfDocEncoding[i] != 0 {
			macExpertEncoding[i] = pdfDocEncoding[i]
		}
	}
}
