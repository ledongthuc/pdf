package pdf

import (
	"bytes"
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
