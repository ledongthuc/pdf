package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

// minimalPDF builds a one-page PDF with a correct cross-reference table;
// edit, when not nil, changes the body before the table is written.
func minimalPDF(edit func(string) string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		"<< /Length 19 >>\nstream\nBT (hello) Tj ET\n\nendstream",
	}
	if edit != nil {
		for i := range objs {
			objs[i] = edit(objs[i])
		}
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

// within fails the test if fn does not return in time: a loop without end
// is the bug these tests guard against.
func within(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }() // a malformed file may panic: that is not this test's concern
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: did not return", name)
	}
}

func open(t *testing.T, b []byte) (*Reader, error) {
	t.Helper()
	return NewReader(bytes.NewReader(b), int64(len(b)))
}

func TestMinimalPDFReads(t *testing.T) {
	r, err := open(t, minimalPDF(nil))
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.Page(1).GetPlainText(nil)
	if err != nil || !strings.Contains(text, "hello") {
		t.Fatalf("%q %v", text, err)
	}
}

// A Pages node that lists itself among its kids.
func TestPageTreeCycle(t *testing.T) {
	b := minimalPDF(func(o string) string { return strings.Replace(o, "/Kids [3 0 R]", "/Kids [2 0 R]", 1) })
	r, err := open(t, b)
	if err != nil {
		t.Fatal(err)
	}
	within(t, "Page", func() {
		if p := r.Page(1); !p.V.IsNull() {
			t.Errorf("a page found in a cycle: %v", p.V)
		}
	})
}

// A page whose Parent is itself: inherited keys are looked up up the
// chain.
func TestParentCycle(t *testing.T) {
	b := minimalPDF(func(o string) string {
		return strings.Replace(o, "/Type /Page /Parent 2 0 R", "/Type /Page /Parent 3 0 R", 1)
	})
	r, err := open(t, b)
	if err != nil {
		t.Fatal(err)
	}
	within(t, "Resources", func() { _ = r.Page(1).Resources() })
}

// A cross-reference section whose /Prev is itself.
func TestXrefPrevCycle(t *testing.T) {
	b := minimalPDF(nil)
	start := bytes.LastIndex(b, []byte("startxref\n")) + len("startxref\n")
	off := string(b[start : start+bytes.IndexByte(b[start:], '\n')])
	b = bytes.Replace(b, []byte("/Root 1 0 R >>"), []byte("/Root 1 0 R /Prev "+off+" >>"), 1)
	within(t, "NewReader", func() {
		if _, err := open(t, b); err == nil {
			t.Error("a looping /Prev chain was accepted")
		}
	})
}

// An object stream that /Extends itself looped forever; one stored inside
// an object stream recursed until the stack overflowed, which no recover
// catches.
func TestObjectStreamCycles(t *testing.T) {
	// The stream at offset 1: an offset of 0 means a free entry.
	body := " 2 0 obj\n<< /Type /ObjStm /N 0 /First 1 /Extends 2 0 R /Length 1 >>\nstream\n \nendstream\nendobj\n"
	r := &Reader{f: strings.NewReader(body), end: int64(len(body))}
	r.xref = []xref{
		{},
		{ptr: objptr{1, 0}, inStream: true, stream: objptr{2, 0}},
		{ptr: objptr{2, 0}, offset: 1},
	}
	within(t, "Extends", func() { _ = r.resolve(objptr{}, objptr{1, 0}) })
}

func TestNestedObjectStream(t *testing.T) {
	r := &Reader{f: strings.NewReader(""), end: 0}
	r.xref = []xref{
		{},
		{ptr: objptr{1, 0}, inStream: true, stream: objptr{2, 0}},
		{ptr: objptr{2, 0}, inStream: true, stream: objptr{2, 0}},
	}
	within(t, "nested object stream", func() {
		if v := r.resolve(objptr{}, objptr{1, 0}); !v.IsNull() {
			t.Errorf("an object found in a nested object stream: %v", v)
		}
	})
}
