package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestNewDictEncoderBaseAndDifferences(t *testing.T) {
	// BaseEncoding is applied first, then Differences overlaid on top.
	e := newDictEncoder(testValue(dict{
		name("BaseEncoding"): name("WinAnsiEncoding"),
		name("Differences"):  array{int64(65), name("Alpha"), name("Beta")},
	}), false)

	// Codes covered by Differences: 65 -> /Alpha, 66 -> /Beta (consecutive names).
	if got := e.Decode("AB"); got != "ΑΒ" {
		t.Fatalf("Decode(AB) = %q, want %q", got, "ΑΒ")
	}
	// Codes not in Differences keep the base encoding: 0x80 is the euro
	// sign in WinAnsiEncoding (and undefined in StandardEncoding).
	if got := e.Decode("\x80"); got != "€" {
		t.Fatalf("Decode(0x80) = %q, want %q (euro from WinAnsi base)", got, "€")
	}
}

func TestNewDictEncoderDifferencesGap(t *testing.T) {
	// A second integer restarts the code counter, leaving a gap untouched.
	e := newDictEncoder(testValue(dict{
		name("BaseEncoding"): name("WinAnsiEncoding"),
		name("Differences"):  array{int64(65), name("Alpha"), int64(67), name("Beta")},
	}), false)
	if got := e.Decode("ABC"); got != "ΑBΒ" {
		t.Fatalf("Decode(ABC) = %q, want %q (66 keeps base value)", got, "ΑBΒ")
	}
}

func TestNewDictEncoderOutOfRangeCode(t *testing.T) {
	// Codes outside 0-255 must be ignored without panicking.
	e := newDictEncoder(testValue(dict{
		name("BaseEncoding"): name("WinAnsiEncoding"),
		name("Differences"):  array{int64(300), name("Alpha"), int64(-1), name("Beta"), int64(65), name("Gamma")},
	}), false)
	if got := e.Decode("A"); got != "Γ" {
		t.Fatalf("Decode(A) = %q, want %q (valid entry after out-of-range ones)", got, "Γ")
	}

	// The code counter must keep advancing past out-of-range codes, so a
	// bad start code doesn't drop later entries that land back in range.
	e2 := newDictEncoder(testValue(dict{
		name("BaseEncoding"): name("WinAnsiEncoding"),
		name("Differences"):  array{int64(-1), name("Alpha"), name("Beta")},
	}), false)
	if got := e2.Decode("\x00"); got != "Β" {
		t.Fatalf("Decode(0x00) = %q, want %q (resynced after out-of-range start)", got, "Β")
	}
}

func TestNewDictEncoderNotdef(t *testing.T) {
	// /.notdef must map to noRune, not silently keep the base value.
	e := newDictEncoder(testValue(dict{
		name("BaseEncoding"): name("WinAnsiEncoding"),
		name("Differences"):  array{int64(65), name(".notdef")},
	}), false)
	if got := e.Decode("A"); got != string(noRune) {
		t.Fatalf("Decode(A) = %q, want %q (noRune for /.notdef)", got, string(noRune))
	}
}

func TestNewDictEncoderDefaultBaseIsStandard(t *testing.T) {
	// Without BaseEncoding the default is StandardEncoding, where 0x27 is
	// quoteright (U+2019), not the apostrophe of PDFDocEncoding/WinAnsi.
	e := newDictEncoder(testValue(dict{}), false)
	if got := e.Decode("'"); got != "’" {
		t.Fatalf("Decode(0x27) = %q, want %q (quoteright in StandardEncoding)", got, "’")
	}
	if got := e.Decode("A"); got != "A" {
		t.Fatalf("Decode(A) = %q, want %q", got, "A")
	}
}

// TestSymbolicFontDifferencesWithoutBaseEncoding covers a symbolic font
// whose Encoding dictionary has Differences but no BaseEncoding. Per PDF
// 32000-1:2008, 9.6.6.1, the implicit base is the font's built-in encoding,
// not StandardEncoding, so codes outside Differences (including 0x80-0x9F,
// which StandardEncoding leaves undefined) keep their raw byte value
// instead of becoming U+FFFD.
func TestSymbolicFontDifferencesWithoutBaseEncoding(t *testing.T) {
	font := func(flags int64) *Font {
		return &Font{V: testValue(dict{
			name("Type"):           name("Font"),
			name("Subtype"):        name("TrueType"),
			name("BaseFont"):       name("ABCDEF+Wingdings"),
			name("FontDescriptor"): dict{name("Flags"): flags},
			name("Encoding"): dict{
				name("Differences"): array{int64(65), name("Alpha")},
			},
		})}
	}

	if got, want := font(4).Encoder().Decode("A'\x80\x92\x9f"), "Α'\u0080\u0092\u009f"; got != want {
		t.Fatalf("symbolic Decode = %q, want %q (Differences, then raw bytes)", got, want)
	}

	// The same font flagged nonsymbolic uses StandardEncoding as its base.
	if got, want := font(32).Encoder().Decode("A'\x80"), "Α’"+string(noRune); got != want {
		t.Fatalf("nonsymbolic Decode = %q, want %q (StandardEncoding base)", got, want)
	}

	// An explicit BaseEncoding is honored even for symbolic fonts.
	e := newDictEncoder(testValue(dict{
		name("BaseEncoding"): name("WinAnsiEncoding"),
	}), true)
	if got := e.Decode("\x80"); got != "€" {
		t.Fatalf("Decode(0x80) = %q, want %q (explicit WinAnsi base)", got, "€")
	}
}

func TestFontIsSymbolic(t *testing.T) {
	tests := []struct {
		name string
		font dict
		want bool
	}{
		{"symbolic flag", dict{name("FontDescriptor"): dict{name("Flags"): int64(4)}}, true},
		{"nonsymbolic flag", dict{name("FontDescriptor"): dict{name("Flags"): int64(32)}}, false},
		{"both flags", dict{name("FontDescriptor"): dict{name("Flags"): int64(36)}}, false},
		{"no flags", dict{name("FontDescriptor"): dict{name("Flags"): int64(0)}}, false},
		{"flags win over name", dict{name("BaseFont"): name("Symbol"), name("FontDescriptor"): dict{name("Flags"): int64(32)}}, false},
		{"standard Symbol", dict{name("BaseFont"): name("Symbol")}, true},
		{"subset ZapfDingbats", dict{name("BaseFont"): name("ABCDEF+ZapfDingbats")}, true},
		{"Helvetica", dict{name("BaseFont"): name("Helvetica")}, false},
	}
	for _, tt := range tests {
		if got := (Font{V: testValue(tt.font)}).isSymbolic(); got != tt.want {
			t.Errorf("%s: isSymbolic() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestEncoderFallbacksWithoutToUnicode pins down how fonts without a
// ToUnicode CMap are decoded, for font kinds the BaseEncoding/symbolic
// changes could affect.
func TestEncoderFallbacksWithoutToUnicode(t *testing.T) {
	tests := []struct {
		name string
		font dict
		raw  string
		want string
	}{
		{
			// No byte-to-rune table applies to 2-byte CIDs, so the raw
			// bytes pass through rather than being decoded one byte at a
			// time as PDFDocEncoding.
			name: "Type0 Identity-H",
			font: dict{
				name("Subtype"):  name("Type0"),
				name("BaseFont"): name("ABCDEF+ArialMT"),
				name("Encoding"): name("Identity-H"),
			},
			raw:  "\x00\x24\x00\x92",
			want: "\x00\x24\x00\x92",
		},
		{
			// Type3 fonts have no FontDescriptor flags to consult, so they
			// are nonsymbolic: Differences codes decode by glyph name and
			// the rest use StandardEncoding.
			name: "Type3 Differences only",
			font: dict{
				name("Subtype"): name("Type3"),
				name("Encoding"): dict{
					name("Differences"): array{int64(1), name("zero"), name("one")},
				},
			},
			raw:  "\x01\x02'",
			want: "01’",
		},
		{
			// A symbolic TrueType font with no Encoding entry uses its
			// built-in (cmap) encoding, which we can't read; it decodes as
			// PDFDocEncoding, as before this change.
			name: "symbolic TrueType without Encoding",
			font: dict{
				name("Subtype"):        name("TrueType"),
				name("BaseFont"):       name("Wingdings-Regular"),
				name("FontDescriptor"): dict{name("Flags"): int64(4)},
			},
			raw:  "Ab",
			want: "Ab",
		},
	}
	for _, tt := range tests {
		f := &Font{V: testValue(tt.font)}
		if got := f.Encoder().Decode(tt.raw); got != tt.want {
			t.Errorf("%s: Decode(%q) = %q, want %q", tt.name, tt.raw, got, tt.want)
		}
	}
}

// TestToUnicodePrecedence verifies that a font's /ToUnicode CMap takes
// precedence over its /Encoding entry (PDF 32000-1:2008, section 9.10.2):
// the CMap remaps 'A' to 'Z' while /Encoding is WinAnsiEncoding, so
// extraction must yield "Z".
func TestToUnicodePrecedence(t *testing.T) {
	toUnicode := "/CIDInit /ProcSet findresource begin\n" +
		"12 dict begin\n" +
		"begincmap\n" +
		"/CMapName /Test-UCS def\n" +
		"1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" +
		"1 beginbfchar\n<41> <005A>\nendbfchar\n" +
		"endcmap\n" +
		"CMapName currentdict /CMap defineresource pop\n" +
		"end end"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(text); got != "Z" {
		t.Fatalf("text = %q, want %q (ToUnicode must win over /Encoding)", got, "Z")
	}
}

// TestMalformedToUnicodeFallsBackToEncoding verifies that a ToUnicode CMap
// with a structural error (endbfchar without beginbfchar) does not panic and
// extraction falls back to the font's /Encoding.
func TestMalformedToUnicodeFallsBackToEncoding(t *testing.T) {
	toUnicode := "begincmap\n" +
		"1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" +
		"endbfchar\n" +
		"endcmap"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want fallback to /Encoding): %v", err)
	}
	if got := strings.TrimSpace(text); got != "A" {
		t.Fatalf("text = %q, want %q (WinAnsi fallback)", got, "A")
	}
}

// TestOversizedCodespaceRangeDoesNotPanic verifies that a ToUnicode CMap
// declaring a codespace range wider than 4 bytes (the maximum the cmap
// decoder supports) is rejected cleanly instead of panicking with an
// index-out-of-range on the space table.
func TestOversizedCodespaceRangeDoesNotPanic(t *testing.T) {
	toUnicode := "begincmap\n" +
		"1 begincodespacerange\n<0000000000> <FFFFFFFFFF>\nendcodespacerange\n" +
		"1 beginbfchar\n<41> <005A>\nendbfchar\n" +
		"endcmap"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want fallback to /Encoding): %v", err)
	}
	if got := strings.TrimSpace(text); got != "A" {
		t.Fatalf("text = %q, want %q (WinAnsi fallback)", got, "A")
	}
}

// TestTruncatedToUnicodeFallsBackToEncoding verifies that a ToUnicode CMap
// cut off inside a beginbfchar section is treated as unparseable, so
// extraction falls back to /Encoding instead of decoding every code to the
// replacement character.
func TestTruncatedToUnicodeFallsBackToEncoding(t *testing.T) {
	toUnicode := "begincmap\n" +
		"1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" +
		"1 beginbfchar\n<41> <005A>"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want fallback to /Encoding): %v", err)
	}
	if got := strings.TrimSpace(text); got != "A" {
		t.Fatalf("text = %q, want %q (WinAnsi fallback)", got, "A")
	}
}

// TestHugeCMapCountRejected verifies that an absurd entry count in a CMap
// section is rejected instead of looping (Pop on an exhausted stack returns
// zero values, so an unchecked count lets a tiny stream allocate unbounded
// entries).
func TestHugeCMapCountRejected(t *testing.T) {
	toUnicode := "begincmap\n" +
		"1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" +
		"9223372036854775807 beginbfchar\nendbfchar\n" +
		"endcmap"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want fallback to /Encoding): %v", err)
	}
	if got := strings.TrimSpace(text); got != "A" {
		t.Fatalf("text = %q, want %q (WinAnsi fallback)", got, "A")
	}
}

// TestMismatchedCMapSectionsRejected verifies that an end operator of the
// wrong kind (beginbfchar closed by endbfrange) is treated as a parse
// failure rather than a matched section.
func TestMismatchedCMapSectionsRejected(t *testing.T) {
	toUnicode := "begincmap\n" +
		"1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" +
		"1 beginbfchar\n<41> <005A>\nendbfrange\n" +
		"endcmap"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want fallback to /Encoding): %v", err)
	}
	if got := strings.TrimSpace(text); got != "A" {
		t.Fatalf("text = %q, want %q (WinAnsi fallback)", got, "A")
	}
}

// TestNoCodespaceFallsBackToEncoding verifies that a CMap with mappings but
// no codespace range is rejected: without a codespace no code can ever
// match, so everything would decode to the replacement character.
func TestNoCodespaceFallsBackToEncoding(t *testing.T) {
	toUnicode := "begincmap\n" +
		"1 beginbfchar\n<41> <005A>\nendbfchar\n" +
		"endcmap"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want fallback to /Encoding): %v", err)
	}
	if got := strings.TrimSpace(text); got != "A" {
		t.Fatalf("text = %q, want %q (WinAnsi fallback)", got, "A")
	}
}

// TestInterpreterPanicFallsBackToEncoding verifies that PostScript-level
// panics inside Interpret (here an unmatched "end") are contained at the
// readCmap boundary so extraction falls back to /Encoding.
func TestInterpreterPanicFallsBackToEncoding(t *testing.T) {
	toUnicode := "begincmap\n" +
		"1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" +
		"1 beginbfchar\n<41> <005A>\nendbfchar\n" +
		"endcmap\nend"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want fallback to /Encoding): %v", err)
	}
	if got := strings.TrimSpace(text); got != "A" {
		t.Fatalf("text = %q, want %q (WinAnsi fallback)", got, "A")
	}
}

// TestMissingSectionOperandRejected verifies that the entry-count check
// only counts operands pushed since the section began: a bfchar entry
// missing its source operand must not pass validation by borrowing
// unrelated values (like the dict begincmap pushes) from the stack.
func TestMissingSectionOperandRejected(t *testing.T) {
	toUnicode := "begincmap\n" +
		"1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" +
		"1 beginbfchar\n<005A>\nendbfchar\n" +
		"endcmap"

	pdfData := simpleFontPDF(toUnicode)
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want fallback to /Encoding): %v", err)
	}
	if got := strings.TrimSpace(text); got != "A" {
		t.Fatalf("text = %q, want %q (WinAnsi fallback)", got, "A")
	}
}

func TestCmapDecodeOddLengthDst(t *testing.T) {
	// A destination that decodes to no UTF-16 units (a single malformed
	// byte) must yield noRune, not silently delete the source character.
	m := &cmap{
		bfchar: []bfchar{{orig: "\x01", repl: "Z"}},
	}
	m.space[0] = []byteRange{{low: "\x00", high: "\xff"}}
	if got := m.Decode("\x01"); got != string(noRune) {
		t.Fatalf("Decode(0x01) = %q, want %q", got, string(noRune))
	}
}

func TestUtf16DecodeOddLength(t *testing.T) {
	// A trailing odd byte must be dropped, not panic on s[i+1].
	if got := utf16Decode("Z"); got != "" {
		t.Fatalf("utf16Decode(%q) = %q, want %q", "Z", got, "")
	}
	if got := utf16Decode("\x00AZ"); got != "A" {
		t.Fatalf("utf16Decode(%q) = %q, want %q", "\x00AZ", got, "A")
	}
}

func TestCmapDecodeEmptyBFRangeDst(t *testing.T) {
	// An empty bfrange destination string has no last byte to scale; it
	// must decode to noRune instead of panicking on b[len(b)-1].
	m := &cmap{
		bfrange: []bfrange{{lo: "\x00", hi: "\xff", dst: testValue("")}},
	}
	m.space[0] = []byteRange{{low: "\x00", high: "\xff"}}
	if got := m.Decode("\x01"); got != string(noRune) {
		t.Fatalf("Decode(0x01) = %q, want %q", got, string(noRune))
	}
}

// simpleFontPDF builds a one-page PDF with a single Type1 font using
// /Encoding /WinAnsiEncoding and the given /ToUnicode stream, whose page
// draws the single character "A".
func simpleFontPDF(toUnicode string) []byte {
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 7)
	writeObject := func(number int, body string) {
		offsets[number] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	writeStream := func(number int, dict, content string) {
		writeObject(number, fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(content), content))
	}

	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 6 0 R >> >> /Contents 4 0 R >>")
	writeStream(4, "", "BT /F1 12 Tf (A) Tj ET")
	writeStream(5, "", toUnicode)
	writeObject(6, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /ToUnicode 5 0 R >>")

	xrefOffset := pdf.Len()
	pdf.WriteString("xref\n0 7\n0000000000 65535 f \n")
	for number := 1; number <= 6; number++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return pdf.Bytes()
}
