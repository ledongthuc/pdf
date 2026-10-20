package pdf

import (
	"strings"
	"testing"
)

// TestFontEncodingRegressionPDF extracts text from testdata/font_encoding.pdf,
// which reproduces the two decoding bugs seen in court filings:
//
//   - F1 is a subset font whose glyph codes were assigned sequentially, with
//     /Encoding /WinAnsiEncoding and a /ToUnicode CMap. Only the CMap gives
//     the right text; decoding with WinAnsi turns the case number into
//     "!\"#$%&'&()*+),-.//)012".
//   - F2 has an Encoding dictionary with /BaseEncoding /WinAnsiEncoding and
//     /Differences moving the digits to codes 1-10. The right single quote
//     (0x92) and en dash (0x96) are not in Differences and must come from the
//     WinAnsi base rather than the raw byte (C1 control characters).
//
// testdata/font_encoding_gen.go regenerates the PDF.
func TestFontEncodingRegressionPDF(t *testing.T) {
	f, r, err := Open("testdata/font_encoding.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	text, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Case 1:15-cv-07433-LAP",
		"Defendants’ reply – filed 03/21/16",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q does not contain %q", text, want)
		}
	}
}
