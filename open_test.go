package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestOpenMissingFile guards against the regression where os.Open failing
// used to call f.Close() on a nil *os.File, turning a simple "file not
// found" into a nil-pointer panic.
func TestOpenMissingFile(t *testing.T) {
	f, r, err := Open("testdata/does-not-exist.pdf")
	if err == nil {
		t.Fatal("expected error opening missing file, got nil")
	}
	if f != nil {
		t.Fatal("expected nil *os.File on open failure")
	}
	if r != nil {
		t.Fatal("expected nil *Reader on open failure")
	}
}

func TestNewReaderRejectsInvalidHeader(t *testing.T) {
	data := []byte("this is not a pdf at all")
	_, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err == nil || !strings.Contains(err.Error(), "invalid header") {
		t.Fatalf("err = %v, want 'invalid header' error", err)
	}
}

// onePagePDF returns a PDF with one empty page that starts with header.
func onePagePDF(header string) []byte {
	var pdf bytes.Buffer
	pdf.WriteString(header)
	offsets := make([]int, 4)
	for number, body := range []string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
	} {
		if number > 0 {
			offsets[number] = pdf.Len()
			fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", number, body)
		}
	}
	xrefOffset := pdf.Len()
	pdf.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for number := 1; number <= 3; number++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return pdf.Bytes()
}

// PDF 2.0 files start with %PDF-2.0, and some writers, such as tiff2pdf (#22),
// put white space between the version and the end of the line.
func TestNewReaderAcceptsHeaders(t *testing.T) {
	for _, header := range []string{"%PDF-1.0\n", "%PDF-1.7\r\n", "%PDF-2.0\n", "%PDF-2.0\r", "%PDF-1.1 \n", "%PDF-1.2\x00\n"} {
		data := onePagePDF(header)
		r, err := NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Errorf("header %q: %v", header, err)
			continue
		}
		if n := r.NumPage(); n != 1 {
			t.Errorf("header %q: NumPage = %d, want 1", header, n)
		}
	}
	for _, header := range []string{"%PDF-1.8\n", "%PDF-2.1\n", "%PDF-1.\n", "%PDF-1.45\n"} {
		data := onePagePDF(header)
		if _, err := NewReader(bytes.NewReader(data), int64(len(data))); err == nil || !strings.Contains(err.Error(), "invalid header") {
			t.Errorf("header %q: err = %v, want 'invalid header' error", header, err)
		}
	}
}

func TestNewReaderRejectsMissingEOF(t *testing.T) {
	// Must be longer than the 100-byte EOF lookback chunk so the tail is
	// actually read (NewReader reads the last 100 bytes).
	data := []byte("%PDF-1.4\n" + strings.Repeat("x", 200))
	_, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err == nil || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("err = %v, want missing %%EOF error", err)
	}
}

func TestNewReaderRejectsMissingStartXref(t *testing.T) {
	data := []byte("%PDF-1.4\n" + strings.Repeat("x", 200) + "\n%%EOF\n")
	_, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err == nil || !strings.Contains(err.Error(), "startxref") {
		t.Fatalf("err = %v, want missing startxref error", err)
	}
}

// encryptedPDFWithBadPassword builds a minimal single-object PDF whose trailer
// carries a Standard-security /Encrypt dictionary (V=1, R=2, 40-bit) with O and
// U values that do not match the empty password. NewReaderEncrypted must then
// consult the password callback, which returns "" to stop immediately.
func encryptedPDFWithBadPassword() []byte {
	// O and U must each be 32 bytes; ID can be any string.
	o := strings.Repeat("O", 32)
	u := strings.Repeat("U", 32)
	id := strings.Repeat("I", 16)

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	xrefOffset := pdf.Len()
	pdf.WriteString("xref\n0 1\n0000000000 65535 f \n")
	fmt.Fprintf(&pdf, "trailer\n<< /Size 1 /ID [(%s)] /Encrypt << /Filter /Standard /V 1 /R 2 /Length 40 /O (%s) /U (%s) /P 0 >> >>\n", id, o, u)
	fmt.Fprintf(&pdf, "startxref\n%d\n%%%%EOF\n", xrefOffset)
	return pdf.Bytes()
}

func TestNewReaderEncryptedStopsOnEmptyPassword(t *testing.T) {
	var calls int
	pw := func() string {
		calls++
		return ""
	}

	data := encryptedPDFWithBadPassword()
	_, err := NewReaderEncrypted(bytes.NewReader(data), int64(len(data)), pw)
	if err != ErrInvalidPassword {
		t.Fatalf("err = %v, want ErrInvalidPassword", err)
	}
	if calls != 1 {
		t.Fatalf("pw called %d times, want 1 (empty string should stop immediately)", calls)
	}
}

func TestEnsureXrefLen(t *testing.T) {
	var table []xref
	table = ensureXrefLen(table, 3)
	if len(table) != 4 || cap(table) < 4 {
		t.Fatalf("len/cap = %d/%d, want len 4", len(table), cap(table))
	}
	table[3] = xref{ptr: objptr{id: 3}}
	// Already-large-enough table is returned unchanged.
	table2 := ensureXrefLen(table, 1)
	if len(table2) != len(table) || table2[3].ptr.id != 3 {
		t.Fatal("ensureXrefLen mutated an already-sufficient table")
	}
}
