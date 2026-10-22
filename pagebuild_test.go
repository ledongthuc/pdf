package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// helveticaFont is a simple font dictionary with WinAnsi encoding and a
// width of 500 for every code from 32 to 126.
var helveticaFont = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding" +
	" /FirstChar 32 /LastChar 126 /Widths [" + strings.Repeat("500 ", 95) + "] >>"

// buildPagePDF constructs a single-page PDF whose page has the given
// resources dictionary and content stream. The extra objects are numbered
// from 5, so resources can refer to them as "5 0 R", "6 0 R", ...
func buildPagePDF(resources, content string, extra ...string) []byte {
	objs := append([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources " + resources + " /Contents 4 0 R >>",
		pdfStream("", content),
	}, extra...)

	var buf bytes.Buffer
	offsets := make([]int, len(objs)+1)
	buf.WriteString("%PDF-1.4\n")
	for i, obj := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}

	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n", len(objs)+1)
	fmt.Fprintf(&buf, "%d\n%%%%EOF\n", xrefOffset)
	return buf.Bytes()
}

// pdfStream formats a stream object whose dictionary has the given entries
// in addition to /Length.
func pdfStream(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// firstPageContent returns the Content of the first page of the PDF in data.
func firstPageContent(t *testing.T, data []byte) Content {
	t.Helper()
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r.Page(1).Content()
}

// contentString concatenates the text of c.
func contentString(c Content) string {
	var b strings.Builder
	for _, t := range c.Text {
		b.WriteString(t.S)
	}
	return b.String()
}
