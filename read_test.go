package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReadObjectMaxDepth(t *testing.T) {
	// Craft a payload of deeply nested "0 0 obj" sequences that would
	// previously cause unbounded recursion and a fatal stack overflow.
	// With the depth limit in place this must produce a recoverable panic
	// (caught here) instead of crashing the process.
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
	// Deeply nested dictionaries: << /A << /A << ... >> >> >>
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
	// Deeply nested arrays: [ [ [ ... ] ] ]
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
	// A moderately nested structure should parse without hitting the limit.
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

func TestNewReaderMaliciousPDF(t *testing.T) {
	// Reproduce the exact attack vector from MM-63434: a PDF with millions
	// of "0 0 obj" tokens that triggers deep recursion during NewReader's
	// xref parsing. With the fix, NewReader should return an error (via
	// recovered panic) rather than crashing the process.
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

// buildPrevLoopPDF constructs a PDF whose cross-reference /Prev chain loops,
// the shape a damaged incremental save leaves behind. Without cycle there is
// one section whose /Prev is its own offset. With cycle the newest section
// lists only the catalog and points to an older one, which lists the page tree
// too and points back to the newest.
func buildPrevLoopPDF(cycle bool) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	catalog := buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	pages := buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n")

	// section lists the first n objects. /Prev is fixed-width, so a
	// section's length does not depend on where it points.
	section := func(n, prev int) string {
		s := fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n%010d 00000 n \n", n, catalog)
		if n > 2 {
			s += fmt.Sprintf("%010d 00000 n \n", pages)
		}
		return s + fmt.Sprintf("trailer\n<< /Size 3 /Root 1 0 R /Prev %010d >>\n", prev)
	}

	older := buf.Len()
	newest := older
	if cycle {
		newest = older + len(section(3, 0))
		buf.WriteString(section(3, newest))
		buf.WriteString(section(2, older))
	} else {
		buf.WriteString(section(3, newest))
	}
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", newest)
	return buf.Bytes()
}

// TestXrefPrevLoopTerminates verifies that NewReader opens a file whose /Prev
// chain loops. readPrevXrefs followed /Prev for as long as it was set, so a
// section pointing back at one already read was parsed again forever.
func TestXrefPrevLoopTerminates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cycle bool
	}{{"self", false}, {"cycle", true}} {
		t.Run(tc.name, func(t *testing.T) {
			data := buildPrevLoopPDF(tc.cycle)
			type result struct {
				r   *Reader
				err error
			}
			done := make(chan result, 1)
			go func() {
				r, err := NewReader(bytes.NewReader(data), int64(len(data)))
				done <- result{r, err}
			}()

			select {
			case res := <-done:
				if res.err != nil {
					t.Fatalf("NewReader: %v", res.err)
				}
				// With cycle, only the older section lists the page tree, so
				// resolving it shows the chain was followed before it ended.
				if got := res.r.Trailer().Key("Root").Key("Pages").Key("Type").Name(); got != "Pages" {
					t.Fatalf("/Root /Pages /Type = %q, want Pages", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("NewReader did not return within 5s: the xref /Prev chain loops")
			}
		})
	}
}
