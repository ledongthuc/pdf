package pdf

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestInterpretContinuesTokensAcrossContentStreams(t *testing.T) {
	pdfData := splitTextArrayPDF()
	reader, err := NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil {
		t.Fatal(err)
	}

	text, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(text); got != "Hello world" {
		t.Fatalf("text = %q, want %q", got, "Hello world")
	}
}

// Tiny readers exercise ownership independently of decoder allocations or GC.
type trackedContentReader struct {
	*strings.Reader
	end   error
	close func()
}

func (r *trackedContentReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if r.Len() == 0 && r.end != nil {
		err = r.end // includes the legal (n > 0, EOF) case
	}
	return n, err
}

func (r *trackedContentReader) Close() error { r.close(); return nil }

func TestContentReaderLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name            string
		parts           []string
		end             error
		stop, openPanic bool
		wantPanic       string
		wantOpen        int
		wantOps         []string
	}{
		{name: "empty array"},
		{name: "split tokens and empty streams", parts: []string{"", "(Hel", "", "lo) 1", "2 em", "it ", ""}, wantOpen: 7, wantOps: []string{"emit"}},
		{name: "data with EOF", parts: []string{"(Hello) 12 emit ", "again "}, end: io.EOF, wantOpen: 2, wantOps: []string{"emit", "again"}},
		{name: "early callback exit", parts: []string{"stop ", "unread "}, stop: true, wantPanic: "stop", wantOpen: 1, wantOps: []string{"stop"}},
		{name: "read error", parts: []string{"", "unread "}, end: io.ErrUnexpectedEOF, wantPanic: "unexpected EOF", wantOpen: 1},
		{name: "data then read error", parts: []string{"again ", "unread "}, end: io.ErrUnexpectedEOF, wantPanic: "unexpected EOF", wantOpen: 1, wantOps: []string{"again"}},
		{name: "open panic after empty", parts: []string{"", ""}, openPanic: true, wantPanic: "open", wantOpen: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active, peak, opened, closed := 0, 0, 0, 0
			r := &contentReader{count: len(tc.parts)}
			r.open = func(i int) io.ReadCloser {
				if active != 0 || i != opened {
					t.Fatal("reader overlap or order changed")
				}
				if tc.openPanic && i == 1 {
					panic("open")
				}
				opened++
				active++
				if active > peak {
					peak = active
				}
				return &trackedContentReader{Reader: strings.NewReader(tc.parts[i]), end: tc.end, close: func() { active--; closed++ }}
			}
			var ops []string
			var caught interface{}
			func() {
				defer func() { caught = recover() }()
				interpretReader(r, func(stk *Stack, op string) {
					ops = append(ops, op)
					if op == "emit" && (stk.Pop().Int64() != 12 || stk.Pop().RawString() != "Hello") {
						t.Fatal("split operands changed")
					}
					if tc.stop {
						panic("stop")
					}
				})
			}()
			if (caught != nil) != (tc.wantPanic != "") || (caught != nil && !strings.Contains(fmt.Sprint(caught), tc.wantPanic)) {
				t.Fatalf("panic = %v, want %q", caught, tc.wantPanic)
			}
			if active != 0 || r.current != nil {
				t.Fatalf("interpreter exit retained reader: active=%d", active)
			}
			r.Close() // idempotent, and reads after close must not reopen anything
			if n, err := r.Read(make([]byte, 1)); n != 0 || err != io.EOF {
				t.Fatalf("read after close = %d, %v", n, err)
			}
			if active != 0 || peak > 1 || opened != tc.wantOpen || closed != opened || r.current != nil {
				t.Fatalf("active=%d peak=%d opened=%d closed=%d", active, peak, opened, closed)
			}
			if !reflect.DeepEqual(ops, tc.wantOps) {
				t.Fatalf("ops=%v, want %v", ops, tc.wantOps)
			}
			t.Logf("max_active=%d opened=%d closed=%d active_after_exit=%d", peak, opened, closed, active)
		})
	}
}

func splitTextArrayPDF() []byte {
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 7)
	writeObject := func(number int, body string) {
		offsets[number] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	writeStream := func(number int, content string) {
		writeObject(number, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content))
	}

	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 6 0 R >> >> /Contents [4 0 R 5 0 R] >>")
	writeStream(4, "BT /F1 12 Tf 20 100 Td [(Hello)")
	writeStream(5, "( world)] TJ ET")
	writeObject(6, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefOffset := pdf.Len()
	pdf.WriteString("xref\n0 7\n0000000000 65535 f \n")
	for number := 1; number <= 6; number++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return pdf.Bytes()
}
