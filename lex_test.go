package pdf

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// buildUnterminatedArrayPDF constructs a minimal single-page PDF whose
// content stream ends inside an array that is never closed. Real-world
// truncated or malformed PDFs exhibit the same shape: the tokenizer hits
// end of input while readArray is still collecting elements.
func buildUnterminatedArrayPDF() []byte {
	// The content stream ends inside "[ ... " with no closing "]".
	return buildContentPDF("BT /F1 12 Tf [ (hello) 1 2")
}

// buildContentPDF constructs a minimal single-page PDF whose content stream
// is content.
func buildContentPDF(content string) []byte {
	var buf bytes.Buffer
	offsets := make([]int, 5)

	buf.WriteString("%PDF-1.4\n")

	objs := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>\nendobj\n",
		fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content),
	}
	for i, obj := range objs {
		offsets[i+1] = buf.Len()
		buf.WriteString(obj)
	}

	xrefOffset := buf.Len()
	buf.WriteString("xref\n0 5\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	buf.WriteString("trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n")
	fmt.Fprintf(&buf, "%d\n%%%%EOF\n", xrefOffset)
	return buf.Bytes()
}

// TestUnterminatedArrayTerminates verifies that text extraction terminates
// on a PDF whose content stream is truncated inside an unterminated array.
// Before readArray handled io.EOF, readToken returned io.EOF as a token
// value, which matched neither nil nor keyword("]"), so readArray appended
// io.EOF objects forever, allocating memory without bound.
func TestUnterminatedArrayTerminates(t *testing.T) {
	data := buildUnterminatedArrayPDF()
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The extracted text is irrelevant; the call just has to return.
		r.GetPlainText()
	}()

	select {
	case <-done:
		// ok: extraction terminated
	case <-time.After(5 * time.Second):
		t.Fatal("GetPlainText did not return within 5s: readArray is looping on io.EOF at end of a truncated content stream")
	}
}

// TestUnterminatedHexStringTerminates verifies that text extraction
// terminates on a PDF whose content stream ends inside a hex string, as it
// does when the stream's /Length is short. At end of input readByte returns
// '\n', and readHexString skipped it as white space for ever.
func TestUnterminatedHexStringTerminates(t *testing.T) {
	for _, content := range []string{
		"BT /F1 12 Tf <4142", // between two pairs of digits
		"BT /F1 12 Tf <414",  // inside a pair
		"BT /F1 12 Tf [ <41", // inside an array too
	} {
		data := buildContentPDF(content)
		r, err := NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("%q: NewReader: %v", content, err)
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			// The extracted text is irrelevant; the call just has to return.
			r.GetPlainText()
		}()

		select {
		case <-done:
			// ok: extraction terminated
		case <-time.After(5 * time.Second):
			t.Fatalf("%q: GetPlainText did not return within 5s: readHexString is looping at the end of a content stream", content)
		}
	}
}

func TestReadInlineImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want dict
	}{
		{
			name: `normal case`,
			data: []byte("BI /W 1 /H 1 ID a EI"),
			want: dict{name("W"): int64(1), name("H"): int64(1), name("ID"): "a"},
		},
		{
			name: `malformed data: BI only`,
			data: []byte("BI"),
			want: dict{name("ID"): ""},
		},
		{
			name: `malformed data: no ID`,
			data: []byte("BI /W 1 /H 1"),
			want: dict{name("W"): int64(1), name("H"): int64(1), name("ID"): ""},
		},
		{
			name: `malformed data: end with ID`,
			data: []byte("BI /W 1 /H 1 ID"),
			want: dict{name("W"): int64(1), name("H"): int64(1), name("ID"): ""},
		},
		{
			name: `malformed data: no image data`,
			data: []byte("BI /W 1 /H 1 ID "),
			want: dict{name("W"): int64(1), name("H"): int64(1), name("ID"): ""},
		},
		{
			name: `malformed data: no EI`,
			data: []byte("BI /W 2 /H 1 ID a"),
			want: dict{name("W"): int64(2), name("H"): int64(1), name("ID"): "a"},
		},
		{
			name: `malformed data: no space before EI`,
			data: []byte("BI /W 1 /H 1 ID aEI Q"),
			want: dict{name("W"): int64(1), name("H"): int64(1), name("ID"): "aEI Q"},
		},
		{
			name: `malformed data: the space between ID and EI is \r\n`,
			data: []byte("BI /W 1 /H 1 ID\r\na\r\nEI"),
			want: dict{name("W"): int64(1), name("H"): int64(1), name("ID"): "\na\r"},
		},
		{
			name: `malformed data: EI in image data`,
			data: []byte("BI /W 3 /H 1 ID  EI EI"),
			want: dict{name("W"): int64(3), name("H"): int64(1), name("ID"): ""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bytes.NewReader(tc.data)
			b := newBuffer(r, 0)
			b.allowEOF = true
			b.allowObjptr = false
			b.allowStream = false

			if tok := b.readToken(); tok != keyword("BI") {
				t.Fatalf("expected BI, got %v", tok)
			}
			got := b.readInlineImage()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
