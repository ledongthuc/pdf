package pdf

import (
	"context"
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

	rc, err := r.GetPlainText(context.Background())
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

func TestPageMediaBox(t *testing.T) {
	data := buildTextPDF("BT /F1 12 Tf (Hello) Tj ET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got := r.Page(1).MediaBox()
	want := [4]float64{0, 0, 612, 792}
	if got != want {
		t.Errorf("MediaBox: want %v, got %v", want, got)
	}
}

func TestPageCropBoxFallback(t *testing.T) {
	data := buildTextPDF("BT /F1 12 Tf (Hello) Tj ET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)
	got := p.CropBox()
	want := p.MediaBox()
	if got != want {
		t.Errorf("CropBox fallback: want %v (MediaBox), got %v", want, got)
	}
}

func buildCropBoxPDF() []byte {
	var b strings.Builder
	offsets := make([]int, 6)

	b.WriteString("%PDF-1.4\n")

	offsets[1] = b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	offsets[2] = b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	offsets[3] = b.Len()
	b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /CropBox [10 20 580 760] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")

	cs := "BT /F1 12 Tf (Hello) Tj ET"
	offsets[4] = b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(cs)+1, cs)

	offsets[5] = b.Len()
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	xrefOff := b.Len()
	fmt.Fprintf(&b, "xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return []byte(b.String())
}

func TestPageCropBoxPresent(t *testing.T) {
	r, err := OpenBytes(buildCropBoxPDF())
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got := r.Page(1).CropBox()
	want := [4]float64{10, 20, 580, 760}
	if got != want {
		t.Errorf("CropBox: want %v, got %v", want, got)
	}
}

func TestPagesIterator(t *testing.T) {
	data := buildOutlinePDF(3, "", 0, 0)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	var indices []int
	for i := range r.Pages() {
		indices = append(indices, i)
	}
	if got, want := len(indices), 3; got != want {
		t.Fatalf("Pages() yielded %d pages, want %d", got, want)
	}
	for j, idx := range indices {
		if idx != j+1 {
			t.Fatalf("page index[%d] = %d, want %d", j, idx, j+1)
		}
	}
}

func TestPagesIteratorBreak(t *testing.T) {
	data := buildOutlinePDF(3, "", 0, 0)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	count := 0
	for range r.Pages() {
		count++
		break
	}
	if count != 1 {
		t.Fatalf("Pages() yielded %d page(s) after break, want 1", count)
	}
}

// twoBlockStream produces two BT blocks at distinct Y positions (700, 600) so
// IsSameSentence returns false and Texts() yields exactly two elements.
const twoBlockStream = "BT /F1 12 Tf 100 700 Td (Hello) Tj ET\nBT /F1 12 Tf 100 600 Td (World) Tj ET"

func TestTextsIterator(t *testing.T) {
	data := buildTextPDF(twoBlockStream)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)
	var texts []Text
	for text := range p.Texts() {
		texts = append(texts, text)
	}
	if got, want := len(texts), 2; got != want {
		t.Fatalf("Texts() yielded %d elements, want %d: %v", got, want, texts)
	}
	if !strings.Contains(texts[0].S, "Hello") {
		t.Errorf("texts[0].S = %q, want to contain 'Hello'", texts[0].S)
	}
	if !strings.Contains(texts[1].S, "World") {
		t.Errorf("texts[1].S = %q, want to contain 'World'", texts[1].S)
	}
}

func TestTextsIteratorBreak(t *testing.T) {
	data := buildTextPDF(twoBlockStream)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	count := 0
	for range r.Page(1).Texts() {
		count++
		break
	}
	if count != 1 {
		t.Fatalf("Texts() yielded %d element(s) after break, want 1", count)
	}
}

// TestTextsMatchesGetStyledTexts verifies that iterating Pages()+Texts() produces
// the same sentences as GetStyledTexts — the equivalence contract stated in the doc comment.
func TestTextsMatchesGetStyledTexts(t *testing.T) {
	data := buildTextPDF(twoBlockStream)
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	want, err := r.GetStyledTexts(context.Background())
	if err != nil {
		t.Fatalf("GetStyledTexts: %v", err)
	}
	var got []Text
	for _, p := range r.Pages() {
		for tx := range p.Texts() {
			got = append(got, tx)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("Pages()+Texts() len=%d, GetStyledTexts len=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].S != want[i].S || got[i].Font != want[i].Font ||
			got[i].FontSize != want[i].FontSize || got[i].X != want[i].X || got[i].Y != want[i].Y {
			t.Errorf("element[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// buildPDFFromObjects assembles a PDF from a slice of raw object bodies.
// Objects are numbered 1..N; objs[0] is object 1, which must be the Catalog.
func buildPDFFromObjects(objs []string) []byte {
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

// formXObjStream returns a PDF stream object string for a Form XObject whose
// content is body, with an optional Resources dict string (e.g. "<< /Font << /F1 7 0 R >> >>").
// Pass "" for resources to get an empty Resources dict.
func formXObjStream(body, resources string) string {
	if resources == "" {
		resources = "<< >>"
	}
	return fmt.Sprintf("<< /Type /XObject /Subtype /Form /Resources %s /Length %d >>\nstream\n%s\nendstream",
		resources, len(body), body)
}

// TestGetPlainTextXObjectForm verifies that text inside a Form XObject
// referenced by Do is extracted by GetPlainText.
func TestGetPlainTextXObjectForm(t *testing.T) {
	xobjBody := "BT (hello) Tj ET"
	pageContent := "/Xi0 Do"
	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page — XObject resource only, no page-level font
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Xi0 5 0 R >> >> /Contents 4 0 R >>",
		// 4: Page content stream
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		// 5: Form XObject
		formXObjStream(xobjBody, ""),
	})
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("GetPlainText = %q; want it to contain %q", got, "hello")
	}
}

// TestGetPlainTextXObjectNestedFont verifies that font names inside a Form
// XObject resolve against the XObject's own /Resources/Font dict, not the
// parent page's font dict.  This is the critical font-rebinding test:
// page /F1 → WinAnsiEncoding (0x80 → '€'), XObject /F1 → MacRomanEncoding
// (0x80 → 'Ä').  GetPlainText must return "€Ä", not "€€" or "ÄÄ".
func TestGetPlainTextXObjectNestedFont(t *testing.T) {
	// \200 is PDF octal escape for byte 0x80.
	pageContent := "BT /F1 12 Tf (\\200) Tj ET /Xi0 Do"
	xobjBody := "BT /F1 12 Tf (\\200) Tj ET"
	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page — page /F1 = WinAnsiEncoding, XObject = Xi0
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> /XObject << /Xi0 5 0 R >> >> /Contents 4 0 R >>",
		// 4: Page content
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		// 5: Form XObject with its own /F1 → MacRomanEncoding (obj 7)
		formXObjStream(xobjBody, "<< /Font << /F1 7 0 R >> >>"),
		// 6: Page /F1 → WinAnsiEncoding (0x80 = '€')
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		// 7: XObject /F1 → MacRomanEncoding (0x80 = 'Ä')
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding >>",
	})
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	// Page uses WinAnsi so 0x80 → '€'; XObject uses MacRoman so 0x80 → 'Ä'.
	if !strings.Contains(got, "€") {
		t.Errorf("GetPlainText = %q; want page byte 0x80 decoded as € (WinAnsi)", got)
	}
	if !strings.Contains(got, "Ä") {
		t.Errorf("GetPlainText = %q; want XObject byte 0x80 decoded as Ä (MacRoman)", got)
	}
}

// TestGetPlainTextXObjectNested verifies that text is extracted from a Form
// XObject that is itself referenced inside another Form XObject (two levels
// of nesting).
func TestGetPlainTextXObjectNested(t *testing.T) {
	innerBody := "BT (deep) Tj ET"
	outerBody := "/Inner Do"
	pageContent := "/Outer Do"
	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page — XObject Outer only
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Outer 4 0 R >> >> /Contents 5 0 R >>",
		// 4: Outer Form XObject — contains /Inner Do
		formXObjStream(outerBody, "<< /XObject << /Inner 6 0 R >> >>"),
		// 5: Page content
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		// 6: Inner Form XObject — contains actual text
		formXObjStream(innerBody, ""),
	})
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(got, "deep") {
		t.Errorf("GetPlainText = %q; want it to contain %q", got, "deep")
	}
}

// TestGetPlainTextImageXObjectIgnored verifies that an Image XObject
// referenced by Do is silently skipped — no panic, no garbage output.
func TestGetPlainTextImageXObjectIgnored(t *testing.T) {
	pageContent := "/Img0 Do"
	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page — Image XObject only
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Img0 4 0 R >> >> /Contents 5 0 R >>",
		// 4: Image XObject (no readable text content)
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /Length 0 >>\nstream\nendstream",
		// 5: Page content
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
	})
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	// Result must be empty (no text in image data) and must not panic.
	if got != "" {
		t.Errorf("GetPlainText = %q; want empty string for image-only XObject", got)
	}
}

// TestGetStyledTextsXObjectForm verifies that text inside a Form XObject is
// returned by GetStyledTexts, exercising the contentState.interpret "Do" path.
// Only the text string is checked; X/Y coordinates are deferred (no /Matrix).
func TestGetStyledTextsXObjectForm(t *testing.T) {
	xobjBody := "BT (world) Tj ET"
	pageContent := "/Xi0 Do"
	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page — XObject resource only
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Xi0 5 0 R >> >> /Contents 4 0 R >>",
		// 4: Page content
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		// 5: Form XObject
		formXObjStream(xobjBody, ""),
	})
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	// contentState appends one Text per glyph; join all S fields to check full word.
	texts := r.Page(1).Content().Text
	var got strings.Builder
	for _, tx := range texts {
		got.WriteString(tx.S)
	}
	if !strings.Contains(got.String(), "world") {
		t.Errorf("Content().Text joined = %q; want it to contain %q", got.String(), "world")
	}
}

// buildWidthsFontPDF returns a minimal PDF whose /F1 font carries explicit
// /FirstChar, /LastChar, and /Widths entries.
// Font /F1: FirstChar=65 ('A'), LastChar=67 ('C'), Widths=[722, 667, 667].
func buildWidthsFontPDF() []byte {
	stream := "BT /F1 12 Tf (ABC) Tj ET"
	return buildPDFFromObjects([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 65 /LastChar 67 /Widths [722 667 667] >>",
	})
}

// TestFontWidths verifies that Widths() returns the /Widths array from the
// font dictionary and satisfies the invariant len == LastChar+1 - FirstChar.
func TestFontWidths(t *testing.T) {
	r, err := OpenBytes(buildWidthsFontPDF())
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	f := r.Page(1).Font("F1")

	widths := f.Widths()
	want := []float64{722, 667, 667}
	if len(widths) != len(want) {
		t.Fatalf("Widths() len = %d, want %d", len(widths), len(want))
	}
	for i, w := range want {
		if widths[i] != w {
			t.Errorf("Widths()[%d] = %v, want %v", i, widths[i], w)
		}
	}
	// invariant: len == LastChar+1 - FirstChar
	first, last := f.FirstChar(), f.LastChar()
	if wantLen := last + 1 - first; len(widths) != wantLen {
		t.Errorf("Widths() len = %d, want %d (LastChar+1-FirstChar = %d+1-%d)",
			len(widths), wantLen, last, first)
	}
}

// TestFontWidthsNoWidthsEntry verifies that Widths() returns an empty slice
// when the font dictionary carries no /Widths entry.
func TestFontWidthsNoWidthsEntry(t *testing.T) {
	r, err := OpenBytes(buildTextPDF("BT /F1 12 Tf (Hi) Tj ET"))
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	if widths := r.Page(1).Font("F1").Widths(); len(widths) != 0 {
		t.Errorf("Widths() on font without /Widths: got %v, want empty", widths)
	}
}

// TestGetTextByColumnPositions verifies that text at a Td position
// is grouped into a column keyed on the integer X coordinate.
func TestGetTextByColumnPositions(t *testing.T) {
	data := buildSinglePagePDF("BT\n100 700 Td\n(AB) Tj\nET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	cols, err := r.Page(1).GetTextByColumn()
	if err != nil {
		t.Fatalf("GetTextByColumn: %v", err)
	}
	if len(cols) == 0 {
		t.Fatal("expected at least one column, got none")
	}
	found := false
	for _, col := range cols {
		for _, txt := range col.Content {
			if txt.S == "AB" {
				found = true
				if txt.X != 100 {
					t.Errorf("text X = %v, want 100", txt.X)
				}
				if txt.Y != 700 {
					t.Errorf("text Y = %v, want 700", txt.Y)
				}
			}
		}
	}
	if !found {
		t.Error("text 'AB' not found in any column")
	}
}

// TestGetTextByColumnTwoColumns verifies that text at two distinct X positions
// produces two separate columns ordered left to right.
func TestGetTextByColumnTwoColumns(t *testing.T) {
	data := buildSinglePagePDF("BT\n100 700 Td\n(Left) Tj\nET\nBT\n300 700 Td\n(Right) Tj\nET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	cols, err := r.Page(1).GetTextByColumn()
	if err != nil {
		t.Fatalf("GetTextByColumn: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("want 2 columns, got %d", len(cols))
	}
	if cols[0].Position >= cols[1].Position {
		t.Errorf("columns not sorted left-to-right: [0].Position=%d [1].Position=%d",
			cols[0].Position, cols[1].Position)
	}
	if len(cols[0].Content) != 1 || cols[0].Content[0].S != "Left" {
		t.Errorf("col[0]: want [Left], got %v", cols[0].Content)
	}
	if len(cols[1].Content) != 1 || cols[1].Content[0].S != "Right" {
		t.Errorf("col[1]: want [Right], got %v", cols[1].Content)
	}
}

// TestGetTextByColumnSortedTopToBottom verifies that multiple texts sharing
// the same X are sorted top to bottom (descending Y, since PDF Y increases
// bottom-to-top).
func TestGetTextByColumnSortedTopToBottom(t *testing.T) {
	data := buildSinglePagePDF("BT\n100 600 Td\n(Lower) Tj\nET\nBT\n100 700 Td\n(Upper) Tj\nET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	cols, err := r.Page(1).GetTextByColumn()
	if err != nil {
		t.Fatalf("GetTextByColumn: %v", err)
	}
	if len(cols) != 1 {
		t.Fatalf("want 1 column, got %d", len(cols))
	}
	content := cols[0].Content
	if len(content) != 2 {
		t.Fatalf("want 2 texts in column, got %d", len(content))
	}
	if content[0].S != "Upper" || content[1].S != "Lower" {
		t.Errorf("want [Upper, Lower] (top-to-bottom), got [%s, %s]", content[0].S, content[1].S)
	}
}

// TestGetTextByColumnNoEmptyEntries verifies that no empty-string Text values
// appear in column output (mirrors TestGetTextByRowNoEmptyRows).
func TestGetTextByColumnNoEmptyEntries(t *testing.T) {
	data := buildSinglePagePDF("BT\n100 700 Td\n(First) Tj\n10 -20 Td\n(Second) Tj\nET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	cols, err := r.Page(1).GetTextByColumn()
	if err != nil {
		t.Fatalf("GetTextByColumn: %v", err)
	}
	for _, col := range cols {
		for _, txt := range col.Content {
			if txt.S == "" {
				t.Errorf("col X=%d: unexpected empty-string Text entry", col.Position)
			}
		}
	}
}
