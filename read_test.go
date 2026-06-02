package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReadObjectMaxDepth(t *testing.T) {
	const depth = maxObjectDepth + 100
	var payload bytes.Buffer
	for i := 0; i < depth; i++ {
		payload.WriteString("0 0 obj\n")
	}
	b := newBuffer(&payload, 0)
	b.allowEOF = true
	panicked := false
	var panicMsg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				panicMsg = r.(error).Error()
			}
		}()
		b.readObject()
	}()
	if !panicked {
		t.Fatal("expected panic from deeply nested objects, but readObject returned normally")
	}
	if !strings.Contains(panicMsg, "maximum depth") {
		t.Fatalf("expected 'maximum depth' in panic message, got: %s", panicMsg)
	}
}

func TestReadDictMaxDepth(t *testing.T) {
	var payload bytes.Buffer
	const depth = maxObjectDepth + 100
	for i := 0; i < depth; i++ {
		payload.WriteString("<< /A ")
	}
	payload.WriteString("null")
	for i := 0; i < depth; i++ {
		payload.WriteString(" >>")
	}
	b := newBuffer(&payload, 0)
	b.allowEOF = true
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		b.readObject()
	}()
	if !panicked {
		t.Fatal("expected panic from deeply nested dicts, but readObject returned normally")
	}
}

func TestReadArrayMaxDepth(t *testing.T) {
	var payload bytes.Buffer
	const depth = maxObjectDepth + 100
	for i := 0; i < depth; i++ {
		payload.WriteString("[ ")
	}
	payload.WriteString("null")
	for i := 0; i < depth; i++ {
		payload.WriteString(" ]")
	}
	b := newBuffer(&payload, 0)
	b.allowEOF = true
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		b.readObject()
	}()
	if !panicked {
		t.Fatal("expected panic from deeply nested arrays, but readObject returned normally")
	}
}

func TestReadObjectNormalDepth(t *testing.T) {
	var payload bytes.Buffer
	const depth = 50
	for i := 0; i < depth; i++ {
		payload.WriteString("<< /A ")
	}
	payload.WriteString("(hello)")
	for i := 0; i < depth; i++ {
		payload.WriteString(" >>")
	}
	b := newBuffer(&payload, 0)
	b.allowEOF = true
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		obj := b.readObject()
		if obj == nil {
			t.Fatal("expected non-nil object from moderately nested dict")
		}
	}()
	if panicked {
		t.Fatal("unexpected panic from moderately nested dicts")
	}
}

// testStream wraps raw bytes as a Value of Kind Stream (no filter).
func testStream(data []byte) Value {
	r := &Reader{f: bytes.NewReader(data), end: int64(len(data))}
	s := stream{hdr: dict{name("Length"): int64(len(data))}, offset: 0}
	return Value{r, objptr{}, s}
}

// TestReadHexStringEOF confirms readHexString terminates when the stream ends
// before the closing '>'.  Without an EOF guard the function spins forever:
// readByte() returns '\n' (a space) after EOF, isSpace('\n') is true, and
// `goto Loop` loops indefinitely without advancing the stream position.
// The input must be all-whitespace after '<' so the loop spins rather than
// hitting the errorf() path (which only fires on invalid hex pairs).
func TestReadHexStringEOF(t *testing.T) {
	// '<' followed by whitespace only — no closing '>'.
	// After all bytes are consumed, readByte returns '\n' (EOF sentinel),
	// isSpace('\n')==true → goto Loop → infinite spin without the fix.
	b := newBuffer(strings.NewReader("<   \t  "), 0)
	b.allowEOF = true

	done := make(chan token, 1)
	go func() { done <- b.readToken() }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readHexString hung: missing EOF guard in readHexString")
	}
}

// TestInterpretInlineImage confirms that Interpret skips inline-image binary
// data (BI … ID <binary> EI) and continues processing operators after EI.
// The binary blob deliberately contains 0x3c ('<') to trigger the lexer path
// that hangs without the ID skip + readHexString EOF guard.
func TestInterpretInlineImage(t *testing.T) {
	binary := []byte{0x3c, 0xff, 0x00, 0x3c, 0x41} // contains '<' (0x3c)
	var buf bytes.Buffer
	buf.WriteString("BI\n/W 10\n/H 10\nID\n")
	buf.Write(binary)
	buf.WriteString("\nEI\n(hello) Tj\n")

	strm := testStream(buf.Bytes())

	type result struct {
		ops []string
		err interface{}
	}
	ch := make(chan result, 1)
	go func() {
		var ops []string
		var panicVal interface{}
		func() {
			defer func() { panicVal = recover() }()
			Interpret(strm, func(stk *Stack, op string) {
				ops = append(ops, op)
			})
		}()
		ch <- result{ops, panicVal}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Interpret panicked: %v", r.err)
		}
		found := false
		for _, op := range r.ops {
			if op == "Tj" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("'Tj' not seen after inline image EI; ops: %v", r.ops)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Interpret hung on content stream with inline image (binary data contains 0x3c)")
	}
}

// buildTextPDF returns a minimal single-page PDF whose content stream is
// contentStream.  The page declares Helvetica (/F1) so Tf operators work.
// Length is len(contentStream)+1 to account for the \n written before endstream.
func buildTextPDF(contentStream string) []byte {
	var b strings.Builder
	offsets := make([]int, 6) // offsets[1..5] for objects 1-5

	b.WriteString("%PDF-1.4\n")

	offsets[1] = b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	offsets[2] = b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	offsets[3] = b.Len()
	b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")

	offsets[4] = b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(contentStream)+1, contentStream)

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

// TestGetPlainTextNoBTNewline is a regression guard for upstream #48:
// a past commit added showText("\n") inside case "BT", prepending a newline
// before every text object.  BT is a matrix-initialisation operator (PDF
// spec §9.4.1) and should not emit whitespace.
func TestGetPlainTextNoBTNewline(t *testing.T) {
	data := buildTextPDF("BT /F1 12 Tf (Hello) Tj ET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("BT operator injected newline into output; got %q", got)
	}
}

// TestGetPlainTextBTNoSeparator pins the pre-826abbb behaviour: adjacent
// BT/ET blocks are not separated by a newline.
func TestGetPlainTextBTNoSeparator(t *testing.T) {
	data := buildTextPDF("BT /F1 12 Tf (Hello) Tj ET\nBT (World) Tj ET")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("adjacent BT/ET blocks should not produce newlines; got %q", got)
	}
}

// TestOpenBytesSpaceAfterHeader confirms that PDFs with a space between the
// version string and the newline (e.g. libtiff/tiff2pdf output) are accepted.
// Upstream issue #22.
func TestOpenBytesSpaceAfterHeader(t *testing.T) {
	// Build a minimal valid PDF but with "%PDF-1.4 \n" header (space before newline).
	base := buildTextPDF("BT (Hi) Tj ET")
	// Replace the header newline with space+newline.
	src := strings.Replace(string(base), "%PDF-1.4\n", "%PDF-1.4 \n", 1)
	_, err := OpenBytes([]byte(src))
	if err != nil {
		t.Fatalf("OpenBytes rejected PDF with space after header: %v", err)
	}
}

// TestOpenBytesEOFBeyond100 confirms that PDFs with %%EOF more than 100 bytes
// before the end (e.g. libtiff-generated files with trailing newlines) are
// accepted under the expanded 1024-byte search window.  Upstream issue #20.
func TestOpenBytesEOFBeyond100(t *testing.T) {
	data := buildTextPDF("BT /F1 12 Tf (hello) Tj ET")
	// Append 400 newlines after %%EOF, pushing it 400 bytes before the end.
	data = append(data, bytes.Repeat([]byte{'\n'}, 400)...)
	if _, err := OpenBytes(data); err != nil {
		t.Fatalf("OpenBytes rejected PDF with %%EOF >100 bytes before end: %v", err)
	}
}

// TestOpenBytesEOFBeyond1024 confirms that PDFs with %%EOF more than 1024 bytes
// before the end are handled by the fallback full-file reverse scan.
// Upstream issue #20.
func TestOpenBytesEOFBeyond1024(t *testing.T) {
	data := buildTextPDF("BT /F1 12 Tf (hello) Tj ET")
	// Append 1500 non-whitespace bytes after %%EOF so TrimRight cannot expose it,
	// forcing the fallback reverse-scan path.
	data = append(data, bytes.Repeat([]byte{'x'}, 1500)...)
	if _, err := OpenBytes(data); err != nil {
		t.Fatalf("OpenBytes rejected PDF with %%EOF >1024 bytes before end: %v", err)
	}
}

func TestNewReaderMaliciousPDF(t *testing.T) {
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.0\n")
	for i := 0; i < 10_000; i++ {
		pdf.WriteString("0\n0\nobj\n")
	}
	pdf.WriteString("startxref\n0\n%%EOF\n")
	data := pdf.Bytes()
	_, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("expected error from malicious PDF, got nil")
	}
}
