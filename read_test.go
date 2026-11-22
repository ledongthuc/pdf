package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestNonStreamPageContents(t *testing.T) {
	pdfData := pdfWithNonStreamContents()
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	page := reader.Page(1)
	text, err := page.GetPlainText(nil)
	if err != nil || text != "" {
		t.Fatalf("GetPlainText = %q, %v; want empty text and no error", text, err)
	}
	content := page.Content()
	if len(content.Text) != 0 || len(content.Rect) != 0 {
		t.Fatalf("Content = %+v, want empty page", content)
	}
}

func TestNonStreamReaderWithoutAllowEOF(t *testing.T) {
	buffer := newBuffer((&Value{}).Reader(), 0)
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(fmt.Sprint(recovered), "stream not present") {
			t.Errorf("readToken panic = %v, want stream not present error", recovered)
		}
	}()
	buffer.readToken()
}

func pdfWithNonStreamContents() []byte {
	var document bytes.Buffer
	document.WriteString("%PDF-1.4\n")
	offsets := make([]int, 5)
	writeObject := func(number int, body string) {
		offsets[number] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", number, body)
	}

	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << >> /Contents 4 0 R >>")
	writeObject(4, "<< /Length 0 >>")

	xrefOffset := document.Len()
	document.WriteString("xref\n0 5\n0000000000 65535 f \n")
	for number := 1; number <= 4; number++ {
		fmt.Fprintf(&document, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&document, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return document.Bytes()
}
