package pdf

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// buildCrossPageFontCachePDF returns a minimal two-page PDF where both pages
// declare the same font resource name /F1 but backed by different font objects:
//
//	page 1: /F1 → MacRomanEncoding  (byte 0x80 → 'Ä', U+00C4)
//	page 2: /F1 → WinAnsiEncoding   (byte 0x80 → '€', U+20AC)
//
// Both pages show the single octet \200 (octal = 0x80) via a Tj operator.
// This structure is the minimal repro for the cross-page font-cache bug in
// (*Reader).GetPlainText: the shared fonts map keyed on the bare name "F1"
// causes page 2 to reuse page 1's MacRoman encoder, decoding 0x80 as 'Ä'
// instead of '€'.
//
// Object layout:
//
//	1: Catalog   /Pages 2 0 R
//	2: Pages     /Kids [3 0 R 4 0 R]
//	3: Page 1    /Resources /Font /F1 6 0 R   /Contents 5 0 R
//	4: Page 2    /Resources /Font /F1 7 0 R   /Contents 5 0 R
//	5: Content stream (shared): BT /F1 12 Tf (\200) Tj ET
//	6: Font /F1 for page 1 — MacRomanEncoding
//	7: Font /F1 for page 2 — WinAnsiEncoding
func buildCrossPageFontCachePDF() []byte {
	// PDF octal escape \200 == byte 0x80.
	// In Go source \\200 is backslash + '2' + '0' + '0' (4 bytes in the string).
	streamBody := "BT /F1 12 Tf (\\200) Tj ET"
	streamLen := len(streamBody) // 25

	objs := []string{
		// 1
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		// 3 — page 1: MacRoman /F1
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> >> /Contents 5 0 R >>",
		// 4 — page 2: WinAnsi /F1 (same resource name, different object)
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 7 0 R >> >> /Contents 5 0 R >>",
		// 5 — content stream shared by both pages
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", streamLen, streamBody),
		// 6 — Font /F1 for page 1
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding >>",
		// 7 — Font /F1 for page 2
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	}

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	off := make([]int, len(objs)+1)
	for i, body := range objs {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefOff := b.Len()
	n := len(objs) + 1
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", n)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", n, xrefOff)
	return []byte(b.String())
}

// buildSinglePagePDF wraps a raw content-stream body in a minimal one-page PDF.
// No font resources are declared; walkTextBlocks falls back to nopEncoder so
// plain ASCII bytes in the stream pass through unchanged.
func buildSinglePagePDF(streamBody string) []byte {
	streamLen := len(streamBody)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", streamLen, streamBody),
	}

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	off := make([]int, len(objs)+1)
	for i, body := range objs {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefOff := b.Len()
	n := len(objs) + 1
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", n)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", n, xrefOff)
	return []byte(b.String())
}

// TestGetTextByRowTdPositions verifies that Td updates Text.X and Text.Y
// (upstream #18). Before the fix all texts had X=0, Y=0.
func TestGetTextByRowTdPositions(t *testing.T) {
	stream := "BT\n100 700 Td\n(AB) Tj\nET"
	data := buildSinglePagePDF(stream)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)
	rows, err := p.GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one row, got none")
	}
	for _, row := range rows {
		for _, txt := range row.Content {
			if txt.S == "" {
				continue
			}
			if txt.X != 100 {
				t.Errorf("Text %q: X = %v, want 100", txt.S, txt.X)
			}
			if txt.Y != 700 {
				t.Errorf("Text %q: Y = %v, want 700", txt.S, txt.Y)
			}
		}
	}
}

// TestGetTextByRowTStar verifies that T* decrements Y by TL while leaving X
// unchanged. Non-zero starting X (100) distinguishes correct behaviour from
// incorrect plan variant that reset X to 0.
func TestGetTextByRowTStar(t *testing.T) {
	stream := "BT\n100 700 Td\n14 TL\n(Line1) Tj\nT*\n(Line2) Tj\nET"
	data := buildSinglePagePDF(stream)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)
	rows, err := p.GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}

	find := func(s string) (Text, bool) {
		for _, row := range rows {
			for _, txt := range row.Content {
				if txt.S == s {
					return txt, true
				}
			}
		}
		return Text{}, false
	}

	line1, ok := find("Line1")
	if !ok {
		t.Fatal("Line1 not found in rows")
	}
	if line1.X != 100 || line1.Y != 700 {
		t.Errorf("Line1: got (%v, %v), want (100, 700)", line1.X, line1.Y)
	}

	line2, ok := find("Line2")
	if !ok {
		t.Fatal("Line2 not found in rows")
	}
	// T* ≡ 0 -TL Td: Y decrements by TL=14; X must remain 100.
	if line2.X != 100 {
		t.Errorf("Line2: X = %v, want 100 (T* must not reset X)", line2.X)
	}
	if line2.Y != 686 {
		t.Errorf("Line2: Y = %v, want 686 (700 - 14)", line2.Y)
	}
}

// TestGetTextByRowNoEmptyRows verifies that Td no longer emits a spurious
// empty-string walker call (upstream #27).
func TestGetTextByRowNoEmptyRows(t *testing.T) {
	stream := "BT\n100 700 Td\n(First) Tj\n10 -20 Td\n(Second) Tj\nET"
	data := buildSinglePagePDF(stream)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)
	rows, err := p.GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}
	for _, row := range rows {
		for _, txt := range row.Content {
			if txt.S == "" {
				t.Errorf("row Y=%d: unexpected empty-string Text entry (spurious Td walker call)", row.Position)
			}
		}
	}
}

// TestGetTextByRowMultiBTResetPosition verifies that BT resets currentX/Y so
// a second text object positioned with Td gets absolute coords, not the
// accumulated offset from the previous object (upstream #18).
func TestGetTextByRowMultiBTResetPosition(t *testing.T) {
	// Two separate BT…ET blocks. Second block uses Td 200 500.
	// Without BT reset: B would land at (100+200, 700+500)=(300,1200).
	// With BT reset:    B lands at (200, 500).
	stream := "BT\n100 700 Td\n(A) Tj\nET\nBT\n200 500 Td\n(B) Tj\nET"
	data := buildSinglePagePDF(stream)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)
	rows, err := p.GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}

	find := func(s string) (Text, bool) {
		for _, row := range rows {
			for _, txt := range row.Content {
				if txt.S == s {
					return txt, true
				}
			}
		}
		return Text{}, false
	}

	a, ok := find("A")
	if !ok {
		t.Fatal("Text 'A' not found")
	}
	if a.X != 100 || a.Y != 700 {
		t.Errorf("A: got (%v, %v), want (100, 700)", a.X, a.Y)
	}

	b, ok := find("B")
	if !ok {
		t.Fatal("Text 'B' not found")
	}
	if b.X != 200 || b.Y != 500 {
		t.Errorf("B: got (%v, %v), want (200, 500)", b.X, b.Y)
	}
}

// TestGetPlainTextCrossPageFontCache verifies that (*Reader).GetPlainText
// resolves fonts per-page rather than reusing a shared cross-page cache.
//
// Both PDF pages declare /F1 with different Encoding values. The same byte
// (0x80) must decode to 'Ä' on page 1 (MacRoman) and '€' on page 2 (WinAnsi).
// Before the fix, both pages decoded to 'Ä' because the stale page-1 encoder
// was held in a shared map keyed on the bare font name "F1".
func TestGetPlainTextCrossPageFontCache(t *testing.T) {
	data := buildCrossPageFontCachePDF()
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	if got := r.NumPage(); got != 2 {
		t.Fatalf("NumPage() = %d, want 2", got)
	}

	rc, err := r.GetPlainText()
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	got := string(raw)

	// page 1 must contain 'Ä' (MacRoman 0x80) and page 2 must contain '€' (WinAnsi 0x80).
	if !strings.Contains(got, "Ä") {
		t.Errorf("page 1: expected 'Ä' (MacRoman 0x80) in output, got %q", got)
	}
	if !strings.Contains(got, "€") {
		t.Errorf("page 2: expected '€' (WinAnsi 0x80) in output, got %q", got)
	}
}

// TestContentBasicText is a snapshot guard for the Content() refactor.
// It verifies that text, position, and rect data are correctly produced by
// the operator dispatch path so any transcription error in the refactored
// handlers is caught immediately.
func TestContentBasicText(t *testing.T) {
	// Build a PDF with Td-positioned text, a rectangle (re), and a TJ kern.
	data := buildTextPDF("q\n10 20 100 50 re\nQ\nBT\n/F1 12 Tf\n50 100 Td\n(Hi) Tj\nET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)
	c := p.Content()

	// Rect from "re"
	if len(c.Rect) != 1 {
		t.Fatalf("Rect: want 1, got %d", len(c.Rect))
	}
	if c.Rect[0].Min.X != 10 || c.Rect[0].Min.Y != 20 {
		t.Errorf("Rect.Min: want (10,20), got (%v,%v)", c.Rect[0].Min.X, c.Rect[0].Min.Y)
	}
	if c.Rect[0].Max.X != 110 || c.Rect[0].Max.Y != 70 {
		t.Errorf("Rect.Max: want (110,70), got (%v,%v)", c.Rect[0].Max.X, c.Rect[0].Max.Y)
	}

	// Text from "Tf + Td + Tj": 2 chars H, i
	if len(c.Text) != 2 {
		t.Fatalf("Text: want 2 chars, got %d", len(c.Text))
	}
	for i, ch := range []string{"H", "i"} {
		if c.Text[i].S != ch {
			t.Errorf("Text[%d].S: want %q, got %q", i, ch, c.Text[i].S)
		}
		if c.Text[i].Font != "Helvetica" {
			t.Errorf("Text[%d].Font: want Helvetica, got %q", i, c.Text[i].Font)
		}
		if c.Text[i].FontSize != 12 {
			t.Errorf("Text[%d].FontSize: want 12, got %v", i, c.Text[i].FontSize)
		}
	}
	// First char must be at the Td position
	if c.Text[0].X != 50 || c.Text[0].Y != 100 {
		t.Errorf("Text[0] pos: want (50,100), got (%v,%v)", c.Text[0].X, c.Text[0].Y)
	}
}
