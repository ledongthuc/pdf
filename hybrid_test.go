package pdf

import (
	"bytes"
	"fmt"
	"testing"
)

// hybridPDF returns a hybrid-reference file whose page tree is in an object
// stream. Its table lists the objects in the stream as free, as writers of such
// files do, and names the cross-reference stream that has them with /XRefStm.
// With badXRefStm, /XRefStm points at the catalog instead.
func hybridPDF(badXRefStm bool) []byte {
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.5\n")
	offsets := make([]int, 6)

	offsets[1] = pdf.Len()
	pdf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	objs := []string{
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
	}
	index := fmt.Sprintf("2 0 3 %d ", len(objs[0])+1)
	content := index + objs[0] + " " + objs[1]
	offsets[4] = pdf.Len()
	fmt.Fprintf(&pdf, "4 0 obj\n<< /Type /ObjStm /N 2 /First %d /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(index), len(content), content)

	// Rows of /W [1 2 1]: type, offset or object stream, generation or index.
	var rows bytes.Buffer
	row := func(typ, field2, field3 int) {
		rows.Write([]byte{byte(typ), byte(field2 >> 8), byte(field2), byte(field3)})
	}
	offsets[5] = pdf.Len()
	row(0, 0, 255)
	row(1, offsets[1], 0)
	row(2, 4, 0)
	row(2, 4, 1)
	row(1, offsets[4], 0)
	row(1, offsets[5], 0)
	fmt.Fprintf(&pdf, "5 0 obj\n<< /Type /XRef /Size 6 /W [1 2 1] /Length %d >>\nstream\n", rows.Len())
	pdf.Write(rows.Bytes())
	pdf.WriteString("\nendstream\nendobj\n")

	xrefStm := offsets[5]
	if badXRefStm {
		xrefStm = offsets[1]
	}
	xrefOffset := pdf.Len()
	pdf.WriteString("xref\n0 6\n0000000000 65535 f \n")
	fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[1])
	pdf.WriteString("0000000000 00001 f \n0000000000 00001 f \n")
	fmt.Fprintf(&pdf, "%010d 00000 n \n%010d 00000 n \n", offsets[4], offsets[5])
	fmt.Fprintf(&pdf, "trailer\n<< /Size 6 /Root 1 0 R /XRefStm %d >>\nstartxref\n%d\n%%%%EOF\n", xrefStm, xrefOffset)
	return pdf.Bytes()
}

func TestHybridReferenceFile(t *testing.T) {
	data := hybridPDF(false)
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if n := r.NumPage(); n != 1 {
		t.Fatalf("NumPage = %d, want 1", n)
	}
	if box := r.Page(1).V.Key("MediaBox"); box.Len() != 4 {
		t.Fatalf("MediaBox = %v, want an array of 4 numbers", box)
	}
}

func TestHybridReferenceFileWithUnreadableXRefStm(t *testing.T) {
	// The table still describes the objects outside object streams.
	data := hybridPDF(true)
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if typ := r.Trailer().Key("Root").Key("Type").Name(); typ != "Catalog" {
		t.Fatalf("Root /Type = %q, want Catalog", typ)
	}
}
