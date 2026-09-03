package render

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"maps"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// debugFontLoad controls whether to print debug info during font loading.
var debugFontLoad = false

// pdfFont represents a parsed PDF font with glyph rendering capability.
type pdfFont struct {
	name      string             // Font name (e.g., "Helvetica", "F1")
	subtype   string             // Font subtype: Type1, TrueType, Type0, Type3
	baseFont  string             // BaseFont name
	sfntFont  *sfnt.Font         // Parsed TrueType/OpenType font (nil for Type1/Type3)
	encoding  map[byte]rune      // Character code to Unicode mapping
	toUnicode map[uint16]rune    // CID to Unicode mapping (for Type0 fonts)
	widths    map[byte]float64   // Character widths in 1/1000 of text space unit
	cidWidths map[uint16]float64 // CID widths for Type0 fonts

	// Font metrics (in 1000 units per em)
	ascent    float64
	descent   float64
	capHeight float64

	// Fallback font for missing glyphs
	fallback *sfnt.Font
}

// loadFont loads and parses a font from the PDF resources.
func (r *Renderer) loadFont(fontDict types.Dict, fontName string) (*pdfFont, error) {
	pf := &pdfFont{
		name:     fontName,
		encoding: make(map[byte]rune),
		widths:   make(map[byte]float64),
	}

	// Get font subtype
	if subtypeObj, found := fontDict.Find("Subtype"); found {
		if name, ok := subtypeObj.(types.Name); ok {
			pf.subtype = name.String()
		}
	}

	// Get BaseFont
	if baseFontObj, found := fontDict.Find("BaseFont"); found {
		if name, ok := baseFontObj.(types.Name); ok {
			pf.baseFont = name.String()
		}
	}

	// Parse encoding
	r.parseFontEncoding(fontDict, pf)

	// Parse widths
	r.parseFontWidths(fontDict, pf)

	// Try to extract embedded font program
	if err := r.extractEmbeddedFont(fontDict, pf); err != nil {
		// Not fatal - we can still render with placeholder or standard font
		if debugFontLoad {
			fmt.Printf("FONT LOAD: %s (subtype=%s, base=%s) - FAILED: %v\n",
				fontName, pf.subtype, pf.baseFont, err)
		}
	} else if debugFontLoad {
		fmt.Printf("FONT LOAD: %s (subtype=%s, base=%s) - sfnt=%v\n",
			fontName, pf.subtype, pf.baseFont, pf.sfntFont != nil)
	}

	// Parse font descriptor for metrics
	r.parseFontDescriptor(fontDict, pf)

	return pf, nil
}

// parseFontEncoding parses the font's encoding to build character code to Unicode mapping.
func (r *Renderer) parseFontEncoding(fontDict types.Dict, pf *pdfFont) {
	// Start with a base encoding
	initStandardEncoding(pf)

	// Check for ToUnicode CMap (most reliable)
	if toUnicodeObj, found := fontDict.Find("ToUnicode"); found {
		if err := r.parseToUnicodeCMap(toUnicodeObj, pf); err == nil {
			return // ToUnicode takes precedence
		}
	}

	// Check for Encoding entry
	encodingObj, found := fontDict.Find("Encoding")
	if !found {
		return
	}

	encodingObj, _ = r.ctx.Dereference(encodingObj)

	switch enc := encodingObj.(type) {
	case types.Name:
		// Named encoding: WinAnsiEncoding, MacRomanEncoding, etc.
		applyNamedEncoding(pf, enc.String())
	case types.Dict:
		// Encoding dictionary with differences
		if baseEnc, found := enc.Find("BaseEncoding"); found {
			if name, ok := baseEnc.(types.Name); ok {
				applyNamedEncoding(pf, name.String())
			}
		}
		if diffsObj, found := enc.Find("Differences"); found {
			r.applyEncodingDifferences(diffsObj, pf)
		}
	}
}

// parseToUnicodeCMap parses a ToUnicode CMap stream.
func (r *Renderer) parseToUnicodeCMap(obj types.Object, pf *pdfFont) error {
	obj, err := r.ctx.Dereference(obj)
	if err != nil {
		return err
	}

	sd, ok := obj.(types.StreamDict)
	if !ok {
		return fmt.Errorf("ToUnicode is not a stream")
	}

	if err := sd.Decode(); err != nil {
		return fmt.Errorf("decode ToUnicode: %w", err)
	}

	// Parse the CMap content
	content := string(sd.Content)

	// Parse beginbfchar...endbfchar sections
	// Format: <srcCode> <dstString>
	bfcharStart := strings.Index(content, "beginbfchar")
	for bfcharStart != -1 {
		bfcharEnd := strings.Index(content[bfcharStart:], "endbfchar")
		if bfcharEnd == -1 {
			break
		}
		section := content[bfcharStart+11 : bfcharStart+bfcharEnd]
		parseBfcharSection(section, pf)
		content = content[bfcharStart+bfcharEnd+9:]
		bfcharStart = strings.Index(content, "beginbfchar")
	}

	// Parse beginbfrange...endbfrange sections
	// Format: <srcCodeLo> <srcCodeHi> <dstStringLo> or <srcCodeLo> <srcCodeHi> [<dst1> <dst2> ...]
	content = string(sd.Content)
	bfrangeStart := strings.Index(content, "beginbfrange")
	for bfrangeStart != -1 {
		bfrangeEnd := strings.Index(content[bfrangeStart:], "endbfrange")
		if bfrangeEnd == -1 {
			break
		}
		section := content[bfrangeStart+12 : bfrangeStart+bfrangeEnd]
		parseBfrangeSection(section, pf)
		content = content[bfrangeStart+bfrangeEnd+10:]
		bfrangeStart = strings.Index(content, "beginbfrange")
	}

	return nil
}

// parseBfcharSection parses a bfchar section of a ToUnicode CMap.
func parseBfcharSection(section string, pf *pdfFont) {
	lines := strings.SplitSeq(section, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse: <srcCode> <dstString>
		parts := extractHexStrings(line)
		if len(parts) < 2 {
			continue
		}

		srcCode := parseHexToUint16(parts[0])
		dstRunes := parseHexToRunes(parts[1])

		if len(dstRunes) > 0 {
			if srcCode < 256 {
				pf.encoding[byte(srcCode)] = dstRunes[0]
			}
			if pf.toUnicode == nil {
				pf.toUnicode = make(map[uint16]rune)
			}
			pf.toUnicode[srcCode] = dstRunes[0]
		}
	}
}

// parseBfrangeSection parses a bfrange section of a ToUnicode CMap.
func parseBfrangeSection(section string, pf *pdfFont) {
	lines := strings.SplitSeq(section, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := extractHexStrings(line)
		if len(parts) < 3 {
			continue
		}

		srcLo := parseHexToUint16(parts[0])
		srcHi := parseHexToUint16(parts[1])

		// Check if third part is an array
		if strings.HasPrefix(parts[2], "[") {
			// Array of destination strings: <srcLo> <srcHi> [<dst1> <dst2> ...]
			// Extract hex strings from the array portion of the line
			arrayStart := strings.Index(line, "[")
			arrayEnd := strings.Index(line, "]")
			if arrayStart != -1 && arrayEnd != -1 && arrayEnd > arrayStart {
				arrayContent := line[arrayStart+1 : arrayEnd]
				dstStrings := extractHexStrings(arrayContent)

				for i, dstHex := range dstStrings {
					src := srcLo + uint16(i)
					if src > srcHi {
						break // Don't go beyond the range
					}
					dstRunes := parseHexToRunes(dstHex)
					if len(dstRunes) > 0 {
						if src < 256 {
							pf.encoding[byte(src)] = dstRunes[0]
						}
						if pf.toUnicode == nil {
							pf.toUnicode = make(map[uint16]rune)
						}
						pf.toUnicode[src] = dstRunes[0]
					}
				}
			}
			continue
		}

		// Single destination string - increment for range
		dstStart := parseHexToUint16(parts[2])

		for src := srcLo; src <= srcHi; src++ {
			dst := rune(dstStart + (src - srcLo))
			if src < 256 {
				pf.encoding[byte(src)] = dst
			}
			if pf.toUnicode == nil {
				pf.toUnicode = make(map[uint16]rune)
			}
			pf.toUnicode[src] = dst
		}
	}
}

// extractHexStrings extracts hex strings (enclosed in <>) from a line.
func extractHexStrings(line string) []string {
	var result []string
	for {
		start := strings.Index(line, "<")
		if start == -1 {
			break
		}
		end := strings.Index(line[start:], ">")
		if end == -1 {
			break
		}
		result = append(result, line[start+1:start+end])
		line = line[start+end+1:]
	}
	return result
}

// parseHexToUint16 parses a hex string to uint16.
func parseHexToUint16(hex string) uint16 {
	val, _ := strconv.ParseUint(hex, 16, 16)
	return uint16(val)
}

// parseHexToRunes parses a hex string to runes.
func parseHexToRunes(hex string) []rune {
	var runes []rune
	// Assume UTF-16BE encoding
	for i := 0; i+3 < len(hex); i += 4 {
		val, err := strconv.ParseUint(hex[i:i+4], 16, 16)
		if err == nil {
			runes = append(runes, rune(val))
		}
	}
	// If that didn't work, try as single-byte
	if len(runes) == 0 && len(hex) >= 2 {
		val, err := strconv.ParseUint(hex, 16, 16)
		if err == nil {
			runes = append(runes, rune(val))
		}
	}
	return runes
}

// applyEncodingDifferences applies encoding differences to the font.
func (r *Renderer) applyEncodingDifferences(diffsObj types.Object, pf *pdfFont) {
	diffsObj, _ = r.ctx.Dereference(diffsObj)
	arr, ok := diffsObj.(types.Array)
	if !ok {
		return
	}

	var currentCode byte
	for _, elem := range arr {
		elem, _ = r.ctx.Dereference(elem)
		switch v := elem.(type) {
		case types.Integer:
			currentCode = byte(v)
		case types.Name:
			// Map glyph name to Unicode
			glyphName := v.String()
			if r := glyphNameToRune(glyphName); r != 0 {
				pf.encoding[currentCode] = r
			}
			currentCode++
		}
	}
}

// parseFontWidths parses the font width information.
func (r *Renderer) parseFontWidths(fontDict types.Dict, pf *pdfFont) {
	// Get FirstChar
	firstCharObj, _ := fontDict.Find("FirstChar")
	firstChar := 0
	if fc, ok := firstCharObj.(types.Integer); ok {
		firstChar = int(fc)
	}

	// Get Widths array
	widthsObj, found := fontDict.Find("Widths")
	if !found {
		// Use default width or metrics from font program
		return
	}

	widthsObj, _ = r.ctx.Dereference(widthsObj)
	widthsArr, ok := widthsObj.(types.Array)
	if !ok {
		return
	}

	for i, w := range widthsArr {
		w, _ = r.ctx.Dereference(w)
		width := 0.0
		switch v := w.(type) {
		case types.Integer:
			width = float64(v)
		case types.Float:
			width = float64(v)
		}
		pf.widths[byte(firstChar+i)] = width
	}
}

// parseFontDescriptor parses the FontDescriptor for font metrics.
func (r *Renderer) parseFontDescriptor(fontDict types.Dict, pf *pdfFont) {
	fdObj, found := fontDict.Find("FontDescriptor")
	if !found {
		return
	}

	fdObj, _ = r.ctx.Dereference(fdObj)
	fd, ok := fdObj.(types.Dict)
	if !ok {
		return
	}

	// Ascent
	if ascObj, found := fd.Find("Ascent"); found {
		pf.ascent = toFloatFromObj(ascObj)
	}

	// Descent
	if descObj, found := fd.Find("Descent"); found {
		pf.descent = toFloatFromObj(descObj)
	}

	// CapHeight
	if capObj, found := fd.Find("CapHeight"); found {
		pf.capHeight = toFloatFromObj(capObj)
	}
}

// extractEmbeddedFont extracts and parses an embedded font program.
func (r *Renderer) extractEmbeddedFont(fontDict types.Dict, pf *pdfFont) error {
	// For Type0 fonts, we need to look at the descendant font
	if pf.subtype == "Type0" {
		return r.extractType0Font(fontDict, pf)
	}

	// Get FontDescriptor
	fdObj, found := fontDict.Find("FontDescriptor")
	if !found {
		return fmt.Errorf("no FontDescriptor")
	}

	fdObj, err := r.ctx.Dereference(fdObj)
	if err != nil {
		return err
	}

	fd, ok := fdObj.(types.Dict)
	if !ok {
		return fmt.Errorf("FontDescriptor is not a dict")
	}

	// Try FontFile2 (TrueType) first
	if ff2Obj, found := fd.Find("FontFile2"); found {
		return r.parseFontFile(ff2Obj, pf, "TrueType")
	}

	// Try FontFile3 (CFF/OpenType)
	if ff3Obj, found := fd.Find("FontFile3"); found {
		return r.parseFontFile(ff3Obj, pf, "CFF")
	}

	// FontFile (Type1) - not supported by sfnt, but we can try
	if ff1Obj, found := fd.Find("FontFile"); found {
		return r.parseFontFile(ff1Obj, pf, "Type1")
	}

	return fmt.Errorf("no embedded font program")
}

// extractType0Font handles Type0 (composite) fonts.
func (r *Renderer) extractType0Font(fontDict types.Dict, pf *pdfFont) error {
	// Get DescendantFonts array
	dfObj, found := fontDict.Find("DescendantFonts")
	if !found {
		return fmt.Errorf("Type0 font has no DescendantFonts")
	}

	dfObj, err := r.ctx.Dereference(dfObj)
	if err != nil {
		return err
	}

	dfArr, ok := dfObj.(types.Array)
	if !ok || len(dfArr) == 0 {
		return fmt.Errorf("invalid DescendantFonts")
	}

	// Get first descendant font
	cidFontObj, err := r.ctx.Dereference(dfArr[0])
	if err != nil {
		return err
	}

	cidFont, ok := cidFontObj.(types.Dict)
	if !ok {
		return fmt.Errorf("CIDFont is not a dict")
	}

	// Parse CID widths
	r.parseCIDWidths(cidFont, pf)

	// Get embedded font from CIDFont's FontDescriptor
	fdObj, found := cidFont.Find("FontDescriptor")
	if !found {
		return fmt.Errorf("CIDFont has no FontDescriptor")
	}

	fdObj, err = r.ctx.Dereference(fdObj)
	if err != nil {
		return err
	}

	fd, ok := fdObj.(types.Dict)
	if !ok {
		return fmt.Errorf("FontDescriptor is not a dict")
	}

	// Try FontFile2 (TrueType/OpenType with TrueType outlines)
	if ff2Obj, found := fd.Find("FontFile2"); found {
		return r.parseFontFile(ff2Obj, pf, "TrueType")
	}

	// Try FontFile3 (CFF/OpenType with CFF outlines)
	if ff3Obj, found := fd.Find("FontFile3"); found {
		return r.parseFontFile(ff3Obj, pf, "CFF")
	}

	return fmt.Errorf("no embedded font in CIDFont")
}

// parseCIDWidths parses CID font widths (W array).
func (r *Renderer) parseCIDWidths(cidFont types.Dict, pf *pdfFont) {
	wObj, found := cidFont.Find("W")
	if !found {
		// Try DW for default width
		if dwObj, found := cidFont.Find("DW"); found {
			dw := toFloatFromObj(dwObj)
			// Store as default
			pf.cidWidths = make(map[uint16]float64)
			pf.cidWidths[0] = dw // Use 0 as placeholder for default
		}
		return
	}

	wObj, _ = r.ctx.Dereference(wObj)
	wArr, ok := wObj.(types.Array)
	if !ok {
		return
	}

	pf.cidWidths = make(map[uint16]float64)

	i := 0
	for i < len(wArr) {
		wArr[i], _ = r.ctx.Dereference(wArr[i])

		cidStart, ok := wArr[i].(types.Integer)
		if !ok {
			i++
			continue
		}
		i++

		if i >= len(wArr) {
			break
		}

		wArr[i], _ = r.ctx.Dereference(wArr[i])

		switch next := wArr[i].(type) {
		case types.Array:
			// Format: c [w1 w2 w3 ...]
			for j, w := range next {
				w, _ = r.ctx.Dereference(w)
				width := toFloatFromObj(w)
				pf.cidWidths[uint16(int(cidStart)+j)] = width
			}
			i++
		case types.Integer:
			// Format: c_first c_last w
			cidEnd := next
			i++
			if i >= len(wArr) {
				break
			}
			wArr[i], _ = r.ctx.Dereference(wArr[i])
			width := toFloatFromObj(wArr[i])
			for cid := int(cidStart); cid <= int(cidEnd); cid++ {
				pf.cidWidths[uint16(cid)] = width
			}
			i++
		default:
			i++
		}
	}
}

// parseFontFile parses an embedded font file stream.
func (r *Renderer) parseFontFile(obj types.Object, pf *pdfFont, fontType string) error {
	obj, err := r.ctx.Dereference(obj)
	if err != nil {
		return err
	}

	sd, ok := obj.(types.StreamDict)
	if !ok {
		return fmt.Errorf("font file is not a stream")
	}

	if err := sd.Decode(); err != nil {
		return fmt.Errorf("decode font file: %w", err)
	}

	// For Type1 fonts, we can't use sfnt
	if fontType == "Type1" {
		return fmt.Errorf("Type1 font programs not supported by sfnt")
	}

	fontData := sd.Content

	// Try to parse, if it fails due to missing post table, patch the font
	font, err := sfnt.Parse(fontData)
	if err != nil {
		// Check if this is a TrueType font missing the 'post' table
		if strings.Contains(err.Error(), "post") {
			patched := patchMissingPostTable(fontData)
			if patched != nil {
				font, err = sfnt.Parse(patched)
				if err != nil {
					return fmt.Errorf("parse patched font: %w", err)
				}
			} else {
				return fmt.Errorf("parse font: %w", err)
			}
		} else {
			return fmt.Errorf("parse font: %w", err)
		}
	}

	pf.sfntFont = font
	return nil
}

// patchMissingPostTable adds a minimal 'post' table v3.0 to a TrueType font
// that is missing one. Returns nil if patching fails.
func patchMissingPostTable(fontData []byte) []byte {
	if len(fontData) < 12 {
		return nil
	}

	// Check if it's a TrueType font (version 0x00010000)
	sfntVersion := binary.BigEndian.Uint32(fontData[0:4])
	if sfntVersion != 0x00010000 {
		return nil
	}

	numTables := binary.BigEndian.Uint16(fontData[4:6])

	// Collect existing table entries
	type tableEntry struct {
		tag      string
		checksum uint32
		offset   uint32
		length   uint32
	}
	var tables []tableEntry

	tableOffset := 12
	hasPost := false
	for range numTables {
		if tableOffset+16 > len(fontData) {
			return nil
		}
		tag := string(fontData[tableOffset : tableOffset+4])
		checksum := binary.BigEndian.Uint32(fontData[tableOffset+4 : tableOffset+8])
		offset := binary.BigEndian.Uint32(fontData[tableOffset+8 : tableOffset+12])
		length := binary.BigEndian.Uint32(fontData[tableOffset+12 : tableOffset+16])

		tables = append(tables, tableEntry{tag, checksum, offset, length})
		if tag == "post" {
			hasPost = true
		}
		tableOffset += 16
	}

	if hasPost {
		return nil // Already has post table
	}

	// Create minimal post table v3.0 (32 bytes)
	// Version 3.0 has no glyph names, just the header
	postTable := make([]byte, 32)
	binary.BigEndian.PutUint32(postTable[0:4], 0x00030000) // version 3.0

	postChecksum := calculateTableChecksum(postTable)

	oldTableDirEnd := 12 + int(numTables)*16
	if oldTableDirEnd > len(fontData) {
		return nil
	}

	dirShift := 16 // One extra table directory entry
	postTableOffset := len(fontData) + dirShift

	// Add post table entry
	tables = append(tables, tableEntry{"post", postChecksum, uint32(postTableOffset), 32})

	// Sort tables alphabetically by tag (required by TrueType spec)
	for i := 0; i < len(tables)-1; i++ {
		for j := i + 1; j < len(tables); j++ {
			if tables[i].tag > tables[j].tag {
				tables[i], tables[j] = tables[j], tables[i]
			}
		}
	}

	newNumTables := uint16(len(tables))
	newSearchRange, newEntrySelector, newRangeShift := calcSearchParams(newNumTables)

	// Build new font
	newFont := make([]byte, 0, len(fontData)+dirShift+len(postTable))

	// Header
	header := make([]byte, 12)
	binary.BigEndian.PutUint32(header[0:4], sfntVersion)
	binary.BigEndian.PutUint16(header[4:6], newNumTables)
	binary.BigEndian.PutUint16(header[6:8], newSearchRange)
	binary.BigEndian.PutUint16(header[8:10], newEntrySelector)
	binary.BigEndian.PutUint16(header[10:12], newRangeShift)
	newFont = append(newFont, header...)

	// Table directory entries (sorted)
	for _, t := range tables {
		entry := make([]byte, 16)
		copy(entry[0:4], []byte(t.tag))
		binary.BigEndian.PutUint32(entry[4:8], t.checksum)
		// Adjust offset for existing tables (they shift by dirShift due to new directory entry)
		offset := t.offset
		if t.tag != "post" {
			offset += uint32(dirShift)
		}
		binary.BigEndian.PutUint32(entry[8:12], offset)
		binary.BigEndian.PutUint32(entry[12:16], t.length)
		newFont = append(newFont, entry...)
	}

	// Copy rest of font data (everything after original table directory)
	newFont = append(newFont, fontData[oldTableDirEnd:]...)

	// Append post table at the end
	newFont = append(newFont, postTable...)

	return newFont
}

// calculateTableChecksum calculates the checksum for a TrueType table.
func calculateTableChecksum(data []byte) uint32 {
	// Pad to 4-byte boundary
	padded := make([]byte, (len(data)+3)&^3)
	copy(padded, data)

	var sum uint32
	for i := 0; i < len(padded); i += 4 {
		sum += binary.BigEndian.Uint32(padded[i : i+4])
	}
	return sum
}

// calcSearchParams calculates searchRange, entrySelector, and rangeShift for TrueType.
func calcSearchParams(numTables uint16) (searchRange, entrySelector, rangeShift uint16) {
	// searchRange = (maximum power of 2 <= numTables) * 16
	// entrySelector = log2(maximum power of 2 <= numTables)
	// rangeShift = numTables * 16 - searchRange
	var power uint16 = 1
	var log uint16 = 0
	for power*2 <= numTables {
		power *= 2
		log++
	}
	searchRange = power * 16
	entrySelector = log
	rangeShift = numTables*16 - searchRange
	return
}

// getGlyph returns the glyph index for a character code.
func (pf *pdfFont) getGlyph(charCode byte) (sfnt.GlyphIndex, rune, bool) {
	// Get Unicode value from encoding
	r, ok := pf.encoding[charCode]
	if !ok {
		r = rune(charCode) // Fallback to raw code
	}

	if pf.sfntFont == nil {
		return 0, r, false
	}

	// Get glyph index from font
	var buf sfnt.Buffer
	glyphIndex, err := pf.sfntFont.GlyphIndex(&buf, r)
	if err != nil || glyphIndex == 0 {
		return 0, r, false
	}

	return glyphIndex, r, true
}

// getGlyphCID returns the glyph index for a CID.
func (pf *pdfFont) getGlyphCID(cid uint16) (sfnt.GlyphIndex, rune, bool) {
	// Get Unicode value from ToUnicode
	r, ok := pf.toUnicode[cid]
	if !ok {
		r = rune(cid) // Fallback
	}

	if pf.sfntFont == nil {
		return 0, r, false
	}

	// For CID fonts, the CID often maps directly to glyph index
	// But we should try Unicode mapping first
	var buf sfnt.Buffer
	glyphIndex, err := pf.sfntFont.GlyphIndex(&buf, r)
	if err != nil || glyphIndex == 0 {
		// Try direct CID mapping
		return sfnt.GlyphIndex(cid), r, true
	}

	return glyphIndex, r, true
}

// getCharWidth returns the width of a character in 1/1000 of text space units.
func (pf *pdfFont) getCharWidth(charCode byte) float64 {
	if w, ok := pf.widths[charCode]; ok {
		return w
	}
	return 600 // Default width (common for monospace)
}

// getCIDWidth returns the width of a CID in 1/1000 of text space units.
func (pf *pdfFont) getCIDWidth(cid uint16) float64 {
	if pf.cidWidths != nil {
		if w, ok := pf.cidWidths[cid]; ok {
			return w
		}
		// Use default width if available
		if w, ok := pf.cidWidths[0]; ok {
			return w
		}
	}
	return 1000 // Default width for CJK fonts
}

// debugGlyphRender controls whether to print debug info during glyph rendering.
var debugGlyphRender = false

// renderGlyph renders a single glyph to the image.
func (pf *pdfFont) renderGlyph(img *image.RGBA, glyphIndex sfnt.GlyphIndex, x, y, fontSize float64, col color.RGBA) float64 {
	if pf.sfntFont == nil {
		return fontSize * 0.6 // Return approximate advance
	}

	// Load glyph segments
	var buf sfnt.Buffer
	ppem := fixed.Int26_6(fontSize * 64) // Convert to 26.6 fixed point

	segments, err := pf.sfntFont.LoadGlyph(&buf, glyphIndex, ppem, nil)
	if err != nil {
		if debugGlyphRender {
			fmt.Printf("LoadGlyph error: glyph=%d err=%v\n", glyphIndex, err)
		}
		return fontSize * 0.6
	}

	if debugGlyphRender && glyphIndex != 0 {
		fmt.Printf("renderGlyph: glyph=%d pos=(%.1f,%.1f) size=%.1f segments=%d\n",
			glyphIndex, x, y, fontSize, len(segments))
	}

	// Get glyph advance width
	advance, err := pf.sfntFont.GlyphAdvance(&buf, glyphIndex, ppem, font.HintingNone)
	if err != nil {
		advance = fixed.Int26_6(fontSize * 0.6 * 64)
	}

	// Rasterize the glyph
	rasterizeGlyph(img, segments, x, y, col)

	return float64(advance) / 64.0
}

// rasterizeGlyph renders glyph segments to an image using the vector rasterizer.
func rasterizeGlyph(img *image.RGBA, segments sfnt.Segments, x, y float64, col color.RGBA) {
	if len(segments) == 0 {
		return
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Create rasterizer
	rast := vector.NewRasterizer(width, height)

	// Debug: track bounds
	var minX, maxX, minY, maxY float32 = 1e9, -1e9, 1e9, -1e9
	updateBounds := func(px, py float32) {
		if px < minX {
			minX = px
		}
		if px > maxX {
			maxX = px
		}
		if py < minY {
			minY = py
		}
		if py > maxY {
			maxY = py
		}
	}

	// Track if we're in a path (for closing before new MoveTo)
	inPath := false
	var startX, startY float32

	// Convert segments to rasterizer path
	// Segments use fixed.Point26_6 (1/64th of a pixel)
	for _, seg := range segments {
		switch seg.Op {
		case sfnt.SegmentOpMoveTo:
			// Close previous path if any
			if inPath {
				rast.LineTo(startX, startY) // Explicitly close the path
			}
			px := float32(x) + float32(seg.Args[0].X)/64.0
			// In TrueType: negative Y = above baseline, positive Y = below baseline
			// In screen coords: Y increases downward
			// So we ADD font Y to screen baseline (negative font Y = smaller screen Y = higher on screen)
			py := float32(y) + float32(seg.Args[0].Y)/64.0
			startX, startY = px, py
			updateBounds(px, py)
			rast.MoveTo(px, py)
			inPath = true

		case sfnt.SegmentOpLineTo:
			px := float32(x) + float32(seg.Args[0].X)/64.0
			py := float32(y) + float32(seg.Args[0].Y)/64.0
			updateBounds(px, py)
			rast.LineTo(px, py)

		case sfnt.SegmentOpQuadTo:
			p1x := float32(x) + float32(seg.Args[0].X)/64.0
			p1y := float32(y) + float32(seg.Args[0].Y)/64.0
			p2x := float32(x) + float32(seg.Args[1].X)/64.0
			p2y := float32(y) + float32(seg.Args[1].Y)/64.0
			updateBounds(p1x, p1y)
			updateBounds(p2x, p2y)
			rast.QuadTo(p1x, p1y, p2x, p2y)

		case sfnt.SegmentOpCubeTo:
			p1x := float32(x) + float32(seg.Args[0].X)/64.0
			p1y := float32(y) + float32(seg.Args[0].Y)/64.0
			p2x := float32(x) + float32(seg.Args[1].X)/64.0
			p2y := float32(y) + float32(seg.Args[1].Y)/64.0
			p3x := float32(x) + float32(seg.Args[2].X)/64.0
			p3y := float32(y) + float32(seg.Args[2].Y)/64.0
			updateBounds(p1x, p1y)
			updateBounds(p2x, p2y)
			updateBounds(p3x, p3y)
			rast.CubeTo(p1x, p1y, p2x, p2y, p3x, p3y)
		}
	}

	// Close the final path
	if inPath {
		rast.LineTo(startX, startY)
	}

	if debugGlyphRender {
		fmt.Printf("  rasterize bounds: min=(%.1f,%.1f) max=(%.1f,%.1f) img=(%dx%d)\n",
			minX, minY, maxX, maxY, width, height)
	}

	// Rasterize to a mask
	rast.ClosePath()

	// Draw using the mask
	rast.Draw(img, img.Bounds(), image.NewUniform(col), image.Point{})
}

// Standard PDF encodings

// initStandardEncoding initializes the font with standard encoding.
func initStandardEncoding(pf *pdfFont) {
	// Initialize with identity mapping for printable ASCII
	for i := byte(32); i < 127; i++ {
		pf.encoding[i] = rune(i)
	}
}

// applyNamedEncoding applies a named encoding to the font.
func applyNamedEncoding(pf *pdfFont, name string) {
	switch name {
	case "WinAnsiEncoding":
		applyWinAnsiEncoding(pf)
	case "MacRomanEncoding":
		applyMacRomanEncoding(pf)
	case "StandardEncoding":
		// Standard encoding is close to ASCII with some differences
		initStandardEncoding(pf)
	}
}

// applyWinAnsiEncoding applies Windows ANSI (CP1252) encoding.
func applyWinAnsiEncoding(pf *pdfFont) {
	// Start with ASCII
	initStandardEncoding(pf)

	// Windows-1252 specific mappings (128-159 range differs from Latin-1)
	winAnsiMappings := map[byte]rune{
		128: '\u20AC', // Euro sign
		130: '\u201A', // Single low-9 quotation mark
		131: '\u0192', // Latin small letter f with hook
		132: '\u201E', // Double low-9 quotation mark
		133: '\u2026', // Horizontal ellipsis
		134: '\u2020', // Dagger
		135: '\u2021', // Double dagger
		136: '\u02C6', // Modifier letter circumflex accent
		137: '\u2030', // Per mille sign
		138: '\u0160', // Latin capital letter S with caron
		139: '\u2039', // Single left-pointing angle quotation
		140: '\u0152', // Latin capital ligature OE
		142: '\u017D', // Latin capital letter Z with caron
		145: '\u2018', // Left single quotation mark
		146: '\u2019', // Right single quotation mark
		147: '\u201C', // Left double quotation mark
		148: '\u201D', // Right double quotation mark
		149: '\u2022', // Bullet
		150: '\u2013', // En dash
		151: '\u2014', // Em dash
		152: '\u02DC', // Small tilde
		153: '\u2122', // Trade mark sign
		154: '\u0161', // Latin small letter s with caron
		155: '\u203A', // Single right-pointing angle quotation
		156: '\u0153', // Latin small ligature oe
		158: '\u017E', // Latin small letter z with caron
		159: '\u0178', // Latin capital letter Y with diaeresis
	}

	maps.Copy(pf.encoding, winAnsiMappings)

	// Latin-1 supplement (160-255) maps directly
	for i := byte(160); i != 0; i++ { // Loop until overflow
		pf.encoding[i] = rune(i)
	}
}

// applyMacRomanEncoding applies Mac Roman encoding.
func applyMacRomanEncoding(pf *pdfFont) {
	// Start with ASCII
	initStandardEncoding(pf)

	// Mac Roman high byte mappings
	macRomanMappings := map[byte]rune{
		128: '\u00C4', 129: '\u00C5', 130: '\u00C7', 131: '\u00C9',
		132: '\u00D1', 133: '\u00D6', 134: '\u00DC', 135: '\u00E1',
		136: '\u00E0', 137: '\u00E2', 138: '\u00E4', 139: '\u00E3',
		140: '\u00E5', 141: '\u00E7', 142: '\u00E9', 143: '\u00E8',
		144: '\u00EA', 145: '\u00EB', 146: '\u00ED', 147: '\u00EC',
		148: '\u00EE', 149: '\u00EF', 150: '\u00F1', 151: '\u00F3',
		152: '\u00F2', 153: '\u00F4', 154: '\u00F6', 155: '\u00F5',
		156: '\u00FA', 157: '\u00F9', 158: '\u00FB', 159: '\u00FC',
		// ... more mappings can be added
	}

	maps.Copy(pf.encoding, macRomanMappings)
}

// glyphNameToRune converts a PostScript glyph name to Unicode.
func glyphNameToRune(name string) rune {
	// Common glyph name mappings
	glyphNames := map[string]rune{
		"space": ' ', "exclam": '!', "quotedbl": '"', "numbersign": '#',
		"dollar": '$', "percent": '%', "ampersand": '&', "quotesingle": '\'',
		"parenleft": '(', "parenright": ')', "asterisk": '*', "plus": '+',
		"comma": ',', "hyphen": '-', "period": '.', "slash": '/',
		"zero": '0', "one": '1', "two": '2', "three": '3',
		"four": '4', "five": '5', "six": '6', "seven": '7',
		"eight": '8', "nine": '9', "colon": ':', "semicolon": ';',
		"less": '<', "equal": '=', "greater": '>', "question": '?',
		"at": '@',
		"A":  'A', "B": 'B', "C": 'C', "D": 'D', "E": 'E', "F": 'F',
		"G": 'G', "H": 'H', "I": 'I', "J": 'J', "K": 'K', "L": 'L',
		"M": 'M', "N": 'N', "O": 'O', "P": 'P', "Q": 'Q', "R": 'R',
		"S": 'S', "T": 'T', "U": 'U', "V": 'V', "W": 'W', "X": 'X',
		"Y": 'Y', "Z": 'Z',
		"bracketleft": '[', "backslash": '\\', "bracketright": ']',
		"asciicircum": '^', "underscore": '_', "grave": '`',
		"a": 'a', "b": 'b', "c": 'c', "d": 'd', "e": 'e', "f": 'f',
		"g": 'g', "h": 'h', "i": 'i', "j": 'j', "k": 'k', "l": 'l',
		"m": 'm', "n": 'n', "o": 'o', "p": 'p', "q": 'q', "r": 'r',
		"s": 's', "t": 't', "u": 'u', "v": 'v', "w": 'w', "x": 'x',
		"y": 'y', "z": 'z',
		"braceleft": '{', "bar": '|', "braceright": '}', "asciitilde": '~',
		// Extended characters
		"bullet": '\u2022', "endash": '\u2013', "emdash": '\u2014',
		"quoteleft": '\u2018', "quoteright": '\u2019',
		"quotedblleft": '\u201C', "quotedblright": '\u201D',
		"fi": '\uFB01', "fl": '\uFB02', "ff": '\uFB00',
		"ffi": '\uFB03', "ffl": '\uFB04',
		"ellipsis": '\u2026', "trademark": '\u2122', "copyright": '\u00A9',
		"registered": '\u00AE', "degree": '\u00B0',
	}

	if r, ok := glyphNames[name]; ok {
		return r
	}

	// Try uniXXXX format
	if strings.HasPrefix(name, "uni") && len(name) == 7 {
		if val, err := strconv.ParseUint(name[3:], 16, 16); err == nil {
			return rune(val)
		}
	}

	return 0
}

// fontCache caches loaded fonts by name.
type fontCache struct {
	ctx   *model.Context
	fonts map[string]*pdfFont
}

// newFontCache creates a new font cache.
func newFontCache(ctx *model.Context) *fontCache {
	return &fontCache{
		ctx:   ctx,
		fonts: make(map[string]*pdfFont),
	}
}

// getFont retrieves a font from the cache or loads it.
func (fc *fontCache) getFont(r *Renderer, res *resources, name string) *pdfFont {
	if font, ok := fc.fonts[name]; ok {
		return font
	}

	// Load font from resources
	if res.dict == nil {
		return nil
	}

	fontDictObj, err := r.getDictEntry(res.dict, "Font")
	if err != nil || fontDictObj == nil {
		return nil
	}

	fontRef, found := fontDictObj.Find(name)
	if !found {
		return nil
	}

	fontRef, err = r.ctx.Dereference(fontRef)
	if err != nil {
		return nil
	}

	fontDict, ok := fontRef.(types.Dict)
	if !ok {
		return nil
	}

	font, err := r.loadFont(fontDict, name)
	if err != nil {
		// Create minimal font info
		font = &pdfFont{
			name:     name,
			encoding: make(map[byte]rune),
			widths:   make(map[byte]float64),
		}
		initStandardEncoding(font)
	}

	fc.fonts[name] = font
	return font
}

// Used for fallback rendering
var defaultFont *sfnt.Font

func init() {
	// We could embed a default font here, but for now we'll rely on
	// placeholder rendering when fonts can't be loaded
}
