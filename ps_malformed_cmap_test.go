package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestInterpretToleratesMalformedCMapPreamble is the regression test for a
// real-world crash: some font subsetting tools emit a /ToUnicode CMap whose
// PostScript preamble is malformed — a /CIDSystemInfo dict literal with a
// stray "def" after every entry, e.g.
//
//	<</Registry (Vendor+Subset+0) def/Ordering (T1UV) def/Supplement 0 def>> def
//
// which is invalid: a dictionary literal ("<< ... >>") should contain only
// key/value pairs, never a "def" token between them. Before this fix,
// Interpret read that literal via readObject, which panicked on the
// unexpected "def" token while parsing what it expected to be a plain
// dictionary — and that panic propagated all the way up through readCmap,
// Font.Encoder, and Page.GetPlainText, failing extraction of the WHOLE page
// even though the font's own beginbfchar/beginbfrange mapping data (the
// part GetPlainText actually needs) was itself complete and well-formed.
//
// Observed in the wild in an Identity-H font on a real health-insurance
// plan document's multi-language notice page (one font per language).
func TestInterpretToleratesMalformedCMapPreamble(t *testing.T) {
	pdfData := malformedCMapPreamblePDF()
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}

	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText returned an error (want the malformed CMap preamble to be tolerated): %v", err)
	}
	if got := strings.TrimSpace(text); got != "AB" {
		t.Fatalf("text = %q, want %q — the font's own bfchar mapping data is well-formed and should still be used", got, "AB")
	}
}

// malformedCMapPreamblePDF builds a one-page PDF with a single Identity-H
// composite font whose /ToUnicode stream's PostScript preamble is malformed
// in the exact shape observed in the wild (see the test's doc comment), but
// whose beginbfchar section — the only part GetPlainText's caller actually
// needs — is well-formed.
func malformedCMapPreamblePDF() []byte {
	// Reproduces the real producer's byte-exact preamble shape (verified
	// against the crashing font's raw /ToUnicode stream): lines separated by
	// "\r", and the /CIDSystemInfo dict literal carrying a stray "def" after
	// every entry.
	toUnicode := "/CIDInit /ProcSet findresource begin\r" +
		"12 dict begin\r" +
		"begincmap\r" +
		"/CIDSystemInfo <<\r" +
		"/Registry (Vendor+Subset+0) def\r" +
		"/Ordering (T1UV) def\r" +
		"/Supplement 0 def\r" +
		">> def\r" +
		"/CMapName /Vendor+Subset+0 def\r" +
		"1 begincodespacerange\r<0000> <FFFF>\rendcodespacerange\r" +
		"2 beginbfchar\r<0001> <0041>\r<0002> <0042>\rendbfchar\r" +
		"endcmap\r" +
		"CMapName currentdict /CMap defineresource pop\r" +
		"end end"

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
	writeStream(4, "", "BT /F1 12 Tf <00010002> Tj ET")
	writeObject(6, "<< /Type /Font /Subtype /Type0 /BaseFont /Vendor+Subset+0 /Encoding /Identity-H /ToUnicode 5 0 R >>")
	writeStream(5, "", toUnicode)

	xrefOffset := pdf.Len()
	pdf.WriteString("xref\n0 7\n0000000000 65535 f \n")
	for number := 1; number <= 6; number++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return pdf.Bytes()
}
