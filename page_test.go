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
