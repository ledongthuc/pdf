package pdf

import (
	"bytes"
	"strings"
	"testing"
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
