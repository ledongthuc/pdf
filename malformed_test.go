// Tests that malformed input is rejected with an error rather than a panic, a
// hang, or an unbounded allocation. Each case below was verified to crash or
// hang before the corresponding bounds check was added.

package pdf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// caseTimeout bounds a single case. The fixed code answers in microseconds; a
// regression that reintroduces an infinite loop shows up as a failure instead
// of a hung CI job.
const caseTimeout = 15 * time.Second

// run calls fn and reports whether it panicked or failed to return in time.
func run(t *testing.T, fn func()) (panicked interface{}, timedOut bool) {
	t.Helper()
	done := make(chan interface{}, 1)
	go func() {
		defer func() { done <- recover() }()
		fn()
	}()
	select {
	case p := <-done:
		return p, false
	case <-time.After(caseTimeout):
		return nil, true
	}
}

// mustNotCrash fails if fn panics or does not return within caseTimeout.
func mustNotCrash(t *testing.T, fn func()) {
	t.Helper()
	if p, timedOut := run(t, fn); timedOut {
		t.Errorf("did not return within %v", caseTimeout)
	} else if p != nil {
		t.Errorf("panicked: %v", p)
	}
}

// openBytes reads data as a PDF, reporting the error rather than the Reader.
func openBytes(data []byte) error {
	_, err := NewReader(bytes.NewReader(data), int64(len(data)))
	return err
}

// pad returns a comment line long enough to push a file past the 100-byte
// window NewReader reads from the end.
func pad() string { return "%" + strings.Repeat("p", 90) + "\n" }

// xrefStreamPDF builds a PDF whose startxref points at an xref stream carrying
// the given extra header entries and raw, unfiltered stream data.
func xrefStreamPDF(hdr, data string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")
	b.WriteString(pad())
	off := b.Len()
	b.WriteString("1 0 obj\n")
	fmt.Fprintf(&b, "<< /Type /XRef /Length %d %s >>\n", len(data), hdr)
	b.WriteString("stream\n")
	b.WriteString(data)
	b.WriteString("\nendstream\nendobj\n")
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off)
	return []byte(b.String())
}

// xrefTablePDF builds a PDF with a classic cross-reference table.
func xrefTablePDF(entries, trailer string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	b.WriteString(pad())
	off := b.Len()
	b.WriteString("xref\n")
	b.WriteString(entries)
	b.WriteString("trailer\n")
	b.WriteString(trailer + "\n")
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off)
	return []byte(b.String())
}

// TestMalformedXref covers the cross-reference bounds checks. Every case here
// previously panicked, or allocated tens of gigabytes trying to preallocate a
// table for a file of a few hundred bytes.
func TestMalformedXref(t *testing.T) {
	entry := "\x01\x09\x00" // one W [1 1 1] entry: type 1, offset 9, gen 0

	tests := []struct {
		name string
		data []byte
	}{
		// /Size preallocates the table directly.
		{"size huge", xrefStreamPDF("/Size 4611686018427387904 /W [1 1 1]", entry)},
		{"size negative", xrefStreamPDF("/Size -1 /W [1 1 1]", entry)},
		{"size past file length", xrefStreamPDF("/Size 100000000 /W [1 1 1]", entry)},

		// /Index names the object numbers a subsection describes.
		{"index start huge", xrefStreamPDF("/Size 1 /W [1 1 1] /Index [4000000000 1]", entry)},
		{"index start negative", xrefStreamPDF("/Size 4 /W [1 1 1] /Index [-1 1]", entry)},
		{"index count negative", xrefStreamPDF("/Size 4 /W [1 1 1] /Index [0 -1]", entry)},
		{"index odd length", xrefStreamPDF("/Size 4 /W [1 1 1] /Index [0]", entry)},

		// /W holds the byte width of each field.
		{"w negative", xrefStreamPDF("/Size 4 /W [-1 2 1]", entry)},
		{"w huge", xrefStreamPDF("/Size 4 /W [2000000000 2000000000 2000000000]", entry)},
		{"w too short", xrefStreamPDF("/Size 4 /W [1 1]", entry)},
		{"w missing", xrefStreamPDF("/Size 4", entry)},

		// Classic cross-reference tables take the same values as tokens.
		{"table start huge", xrefTablePDF("4000000000 1\n0000000016 00000 n \n", "<< /Size 5 >>")},
		{"table start negative", xrefTablePDF("-1 1\n0000000016 00000 n \n", "<< /Size 5 >>")},
		{"table count negative", xrefTablePDF("0 -1\n0000000016 00000 n \n", "<< /Size 5 >>")},
		{"trailer size negative", xrefTablePDF("0 1\n0000000000 65535 f \n", "<< /Size -1 >>")},
		{"trailer size missing", xrefTablePDF("0 1\n0000000000 65535 f \n", "<< /Root 1 0 R >>")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			mustNotCrash(t, func() { err = openBytes(tt.data) })
			if err == nil {
				t.Error("accepted a malformed PDF, want an error")
			}
		})
	}

	// An /Index that reaches past the preallocated table is tolerated, the
	// same way readXrefTableData tolerates a classic subsection past the end:
	// the table grows, now within a bound. Growing capacity does not
	// necessarily grow length past the index, which is what used to panic.
	t.Run("index start past table grows safely", func(t *testing.T) {
		for _, start := range []int{1, 5, 9, 100, 1000} {
			hdr := fmt.Sprintf("/Size 0 /W [1 1 1] /Index [%d 1]", start)
			mustNotCrash(t, func() { openBytes(xrefStreamPDF(hdr, entry)) })
		}
	})
}

// TestMalformedFileStructure covers the checks around the header, the trailing
// %%EOF window and startxref.
func TestMalformedFileStructure(t *testing.T) {
	// A file ending in newlines emptied the end-of-file buffer and then read
	// buf[-1], because && binds tighter than || in the strip loop.
	trailingNewlines := append([]byte("%PDF-1.5\n"), bytes.Repeat([]byte("x"), 50)...)
	trailingNewlines = append(trailingNewlines, bytes.Repeat([]byte("\n"), 100)...)

	tests := []struct {
		name string
		data []byte
	}{
		{"trailing newlines", trailingNewlines},
		{"all newlines after header", append([]byte("%PDF-1.5\n"), bytes.Repeat([]byte("\n"), 120)...)},
		{"empty", nil},
		{"header only", []byte("%PDF-1.5\n")},
		{"truncated", []byte("%PDF-1.5\n" + pad() + "startxref\n9\n%%EOF\n")},
		{"startxref huge", []byte("%PDF-1.5\n" + pad() + "startxref\n999999999999\n%%EOF\n")},
		{"startxref negative", []byte("%PDF-1.5\n" + pad() + "startxref\n-5\n%%EOF\n")},
		{"startxref not a number", []byte("%PDF-1.5\n" + pad() + "startxref\nxyz\n%%EOF\n")},
		{"no eof marker", []byte("%PDF-1.5\n" + pad() + "startxref\n9\n")},
		{"bad version", []byte("%PDF-9.9\n" + pad() + "startxref\n9\n%%EOF\n")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			mustNotCrash(t, func() { err = openBytes(tt.data) })
			if err == nil {
				t.Error("accepted a malformed PDF, want an error")
			}
		})
	}
}

// TestLargeXrefTableGrows checks that a table bigger than the preallocation cap
// is still built in full, by growing as entries are actually read. Capping the
// preallocation must not cap the table.
func TestLargeXrefTableGrows(t *testing.T) {
	const n = maxXrefPrealloc + 5000

	var entries bytes.Buffer
	for i := 0; i < n; i++ {
		entries.Write([]byte{1, 9, 0}) // type 1, offset 9, generation 0
	}
	data := xrefStreamPDF(fmt.Sprintf("/Size %d /W [1 1 1]", n), entries.String())

	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := len(r.xref); got != n {
		t.Errorf("xref table has %d entries, want %d", got, n)
	}
}

// TestCompressedXrefStreamManyObjects is why the table is not bounded by the
// file length. An xref stream is compressed, so a file of a few hundred bytes
// can legitimately describe a hundred thousand objects, and a length-derived
// bound would reject it.
func TestCompressedXrefStreamManyObjects(t *testing.T) {
	const n = 100000

	var raw bytes.Buffer
	for i := 0; i < n; i++ {
		raw.Write([]byte{1, 9, 0})
	}
	var comp bytes.Buffer
	zw := zlib.NewWriter(&comp)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	hdr := fmt.Sprintf("/Size %d /W [1 1 1] /Filter /FlateDecode", n)
	data := xrefStreamPDF(hdr, comp.String())
	if len(data) > 8192 {
		t.Fatalf("test file grew to %d bytes; it needs to stay far smaller than %d objects", len(data), n)
	}

	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader on a %d-byte file describing %d objects: %v", len(data), n, err)
	}
	if got := len(r.xref); got != n {
		t.Errorf("xref table has %d entries, want %d", got, n)
	}
}

// stream returns a Value of Kind Stream holding content, with no filter.
func rawStream(content string) Value {
	r := &Reader{f: bytes.NewReader([]byte(content)), end: int64(len(content))}
	return Value{r, objptr{}, stream{dict{name("Length"): int64(len(content))}, objptr{}, 0}}
}

// TestMalformedLexer covers tokenizer termination.
func TestMalformedLexer(t *testing.T) {
	// readByte reports end of input as '\n' once allowEOF is set, so a hex
	// string with no closing '>' used to skip whitespace forever.
	for _, content := range []string{"<AB", "<AB ", "<", "<A", "< ", "<AB\n\n\n"} {
		t.Run("unterminated hex string "+content, func(t *testing.T) {
			mustNotCrash(t, func() {
				Interpret(rawStream(content), func(stk *Stack, op string) {})
			})
		})
	}

	// These are rejected by design. What matters is that they terminate: the
	// panic is the library's own error signal, reported to callers by the
	// public APIs that recover.
	for _, content := range []string{"(unterminated", "(nested (deeper", "/name#", "/name#z", "<AB<CD", "<AG>"} {
		t.Run("rejected "+content, func(t *testing.T) {
			if _, timedOut := run(t, func() {
				Interpret(rawStream(content), func(stk *Stack, op string) {})
			}); timedOut {
				t.Errorf("did not return within %v", caseTimeout)
			}
		})
	}
}

// TestMalformedContentStream covers the content stream operators.
func TestMalformedContentStream(t *testing.T) {
	// Q with nothing saved indexed gstack[-1].
	for _, content := range []string{"Q", "q Q Q", "Q Q Q", "q q Q Q Q"} {
		t.Run("unbalanced "+content, func(t *testing.T) {
			mustNotCrash(t, func() { pageWithContent(content).Content() })
		})
	}

	// Operators given the wrong number of operands panic by design. They must
	// be reported through the APIs that return an error, not escape them.
	for _, content := range []string{"1 2 Tm", "Tm", "1 2 3 cm", "re", "1 Tj", "TJ"} {
		t.Run("bad operands "+content, func(t *testing.T) {
			mustNotCrash(t, func() {
				if _, err := pageWithContent(content).GetPlainText(nil); err != nil {
					t.Logf("reported: %v", err)
				}
			})
			mustNotCrash(t, func() {
				if _, err := pageWithContent(content).GetTextByRow(); err != nil {
					t.Logf("reported: %v", err)
				}
			})
			mustNotCrash(t, func() {
				if _, err := pageWithContent(content).GetTextByColumn(); err != nil {
					t.Logf("reported: %v", err)
				}
			})
		})
	}
}

// pageWithContent builds a Page whose /Contents is an unfiltered stream.
func pageWithContent(content string) Page {
	r := &Reader{f: bytes.NewReader([]byte(content)), end: int64(len(content))}
	strm := stream{dict{name("Length"): int64(len(content))}, objptr{}, 0}
	return Page{V: Value{r, objptr{}, dict{name("Contents"): strm}}}
}

// TestGetTextByRowColumnReportErrors pins the named-return fix: a panic has to
// surface as an error, not as a silent empty result.
func TestGetTextByRowColumnReportErrors(t *testing.T) {
	// "Tm" with no operands panics inside walkTextBlocks.
	p := pageWithContent("Tm")

	if _, err := p.GetTextByRow(); err == nil {
		t.Error("GetTextByRow: got nil error, want the recovered panic")
	}
	if _, err := p.GetTextByColumn(); err == nil {
		t.Error("GetTextByColumn: got nil error, want the recovered panic")
	}
}

// TestMalformedCmap covers the ToUnicode CMap parser.
func TestMalformedCmap(t *testing.T) {
	const space = "1 begincodespacerange <00> <ff> endcodespacerange "

	tests := []struct {
		name    string
		content string
	}{
		// A codespace range wider than 4 bytes indexed cmap.space[4].
		{"codespace too wide", "1 begincodespacerange <0102030405> <0102030406> endcodespacerange"},
		{"codespace mismatched", "1 begincodespacerange <00> <ffff> endcodespacerange"},
		{"codespace empty", "1 begincodespacerange <> <> endcodespacerange"},
		{"codespace count over", "9 begincodespacerange <00> <ff> endcodespacerange"},
		{"codespace no begin", "endcodespacerange"},

		// Declared counts used to be trusted, so a few digits appended
		// hundreds of millions of empty entries.
		{"bfchar count over", space + "268435456 beginbfchar endbfchar"},
		{"bfchar count huge", space + "9223372036854775807 beginbfchar endbfchar"},
		{"bfchar count negative", space + "-1 beginbfchar endbfchar"},
		{"bfchar no begin", space + "endbfchar"},
		{"bfrange count over", space + "268435456 beginbfrange endbfrange"},
		{"bfrange count huge", space + "9223372036854775807 beginbfrange endbfrange"},
		{"bfrange no begin", space + "endbfrange"},

		// Interpret's own panics must not escape readCmap.
		{"end without begin", "end"},
		{"begin non dict", "1 begin"},
		{"def without dict", "/k 1 def"},
		{"currentdict", "currentdict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustNotCrash(t, func() {
				if m := readCmap(rawStream(tt.content)); m != nil {
					// Whatever was salvaged must also be safe to use.
					m.Decode("A")
					if n := len(m.bfchar) + len(m.bfrange); n > 1024 {
						t.Errorf("built %d entries from %d bytes of input", n, len(tt.content))
					}
				}
			})
		})
	}
}

// TestMalformedCmapDecode covers using a cmap that parsed successfully but
// holds hostile values.
func TestMalformedCmapDecode(t *testing.T) {
	const space = "1 begincodespacerange <00> <ff> endcodespacerange "

	tests := []struct {
		name    string
		content string
		decode  string
	}{
		// An odd-length replacement read one byte past the end in utf16Decode.
		{"odd length bfchar", space + "1 beginbfchar <41> <414243> endbfchar", "A"},
		{"odd length bfrange", space + "1 beginbfrange <41> <5a> <414243> endbfrange", "A"},
		// An empty destination has no low byte to scale, and indexed b[-1].
		{"empty bfrange dst", space + "1 beginbfrange <41> <5a> () endbfrange", "B"},
		{"empty bfchar repl", space + "1 beginbfchar <41> () endbfchar", "A"},
		{"bfrange array short", space + "1 beginbfrange <41> <5a> [<0041>] endbfrange", "Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustNotCrash(t, func() {
				if m := readCmap(rawStream(tt.content)); m != nil {
					m.Decode(tt.decode)
				}
			})
		})
	}
}

// TestUTF16DecodeOddLength pins the direct fix: an odd trailing byte cannot
// form a code unit and is dropped rather than read past.
func TestUTF16DecodeOddLength(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"\x00A", "A"},
		{"A", ""},                // lone byte, dropped
		{"\x00A\x00B", "AB"},     //
		{"\x00A\x00B\x00", "AB"}, // trailing byte dropped
		{"\x00A\x00B\x00C\x00", "ABC"},
	}
	for _, tt := range tests {
		if got := utf16Decode(tt.in); got != tt.want {
			t.Errorf("utf16Decode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestCyclicReferences covers the traversals over object graphs a file can
// point back at themselves. The outline case previously exhausted the goroutine
// stack, which is a fatal error no caller can recover from.
func TestCyclicReferences(t *testing.T) {
	newReader := func() *Reader { return &Reader{f: bytes.NewReader(nil), end: 0} }

	t.Run("parent chain", func(t *testing.T) {
		d := dict{}
		d[name("Parent")] = d
		mustNotCrash(t, func() {
			Page{V: Value{newReader(), objptr{}, d}}.Resources()
		})
	})

	t.Run("parent chain long", func(t *testing.T) {
		// A chain longer than the cap but not cyclic still terminates.
		leaf := dict{name("Resources"): dict{}}
		cur := leaf
		for i := 0; i < 500; i++ {
			cur = dict{name("Parent"): cur}
		}
		mustNotCrash(t, func() {
			Page{V: Value{newReader(), objptr{}, cur}}.Resources()
		})
	})

	t.Run("page kids", func(t *testing.T) {
		node := dict{name("Type"): name("Pages"), name("Count"): int64(10)}
		node[name("Kids")] = array{node}
		r := newReader()
		r.trailer = dict{name("Root"): dict{name("Pages"): node}}
		mustNotCrash(t, func() { r.Page(5) })
	})

	t.Run("outline first", func(t *testing.T) {
		d := dict{name("Title"): "t"}
		d[name("First")] = d
		r := newReader()
		r.trailer = dict{name("Root"): dict{name("Outlines"): d}}
		mustNotCrash(t, func() { r.Outline() })
	})

	t.Run("outline next", func(t *testing.T) {
		child := dict{name("Title"): "c"}
		child[name("Next")] = child
		d := dict{name("Title"): "t", name("First"): child}
		r := newReader()
		r.trailer = dict{name("Root"): dict{name("Outlines"): d}}
		mustNotCrash(t, func() { r.Outline() })
	})
}

// TestSeekForwardOutOfRange pins the buffer position check. A negative or
// backwards seek left buf.pos negative, which indexed buf out of bounds on the
// next read.
func TestSeekForwardOutOfRange(t *testing.T) {
	for _, off := range []int64{-1, -1 << 40} {
		b := newBuffer(strings.NewReader("hello world"), 0)
		b.allowEOF = true
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("seekForward(%d): no error reported", off)
				}
			}()
			b.seekForward(off)
			b.readByte()
		}()
	}
}

// validPDF builds a small, well-formed PDF with one page of text.
func validPDF() []byte {
	const content = "BT /F1 24 Tf 100 700 Td (Hello World) Tj ET\n"

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R /MediaBox [0 0 612 792] >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	}

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	xrefOff := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objs)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\n", len(objs)+1)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return []byte(b.String())
}

// TestValidPDFStillParses guards against the bounds checks rejecting
// well-formed input.
func TestValidPDFStillParses(t *testing.T) {
	data := validPDF()

	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if n := r.NumPage(); n != 1 {
		t.Errorf("NumPage = %d, want 1", n)
	}

	p := r.Page(1)
	if p.V.IsNull() {
		t.Fatal("Page(1) is null")
	}
	if got := p.V.Key("Type").Name(); got != "Page" {
		t.Errorf("page /Type = %q, want %q", got, "Page")
	}
	// Resources is inherited through /Parent, exercising findInherited.
	if fonts := p.Fonts(); len(fonts) != 1 || fonts[0] != "F1" {
		t.Errorf("Fonts = %v, want [F1]", fonts)
	}
	if got := p.Font("F1").BaseFont(); got != "Helvetica" {
		t.Errorf("BaseFont = %q, want %q", got, "Helvetica")
	}

	text, err := p.GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(text, "Hello World") {
		t.Errorf("GetPlainText = %q, want it to contain %q", text, "Hello World")
	}

	rd, err := r.GetPlainText()
	if err != nil {
		t.Fatalf("Reader.GetPlainText: %v", err)
	}
	all, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading text: %v", err)
	}
	if !strings.Contains(string(all), "Hello World") {
		t.Errorf("Reader.GetPlainText = %q, want it to contain %q", all, "Hello World")
	}

	if c := p.Content(); len(c.Text) == 0 {
		t.Error("Content returned no text")
	}
	if _, err := r.GetStyledTexts(); err != nil {
		t.Errorf("GetStyledTexts: %v", err)
	}
	if _, err := p.GetTextByRow(); err != nil {
		t.Errorf("GetTextByRow: %v", err)
	}
	if _, err := p.GetTextByColumn(); err != nil {
		t.Errorf("GetTextByColumn: %v", err)
	}
	r.Outline() // must not panic
}

// objStmPDF builds a well-formed PDF that keeps its catalog, page tree and page
// inside an object stream, indexed by a cross-reference stream. This is the
// layout every /Size, /W, /Index and /N check has to keep working.
func objStmPDF() []byte { return objStmPDFWith("") }

// objStmPDFWith is objStmPDF with the object stream's /N and /First replaced by
// hdr, to exercise the checks on those values. An empty hdr keeps the correct
// ones.
func objStmPDFWith(hdr string) []byte {
	const content = "BT /F1 24 Tf 100 700 Td (Hello Stream) Tj ET\n"

	inner := []struct {
		num  int
		body string
	}{
		{1, "<< /Type /Catalog /Pages 2 0 R >>"},
		{2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"},
		{3, "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /MediaBox [0 0 612 792] >>"},
	}

	// An object stream holds a table of "number offset" pairs, then the
	// objects themselves starting at /First.
	var pairs, bodies strings.Builder
	for _, o := range inner {
		fmt.Fprintf(&pairs, "%d %d ", o.num, bodies.Len())
		bodies.WriteString(o.body + "\n")
	}
	first := pairs.Len()
	objstm := pairs.String() + bodies.String()

	var b strings.Builder
	b.WriteString("%PDF-1.5\n")
	off := map[int]int{}

	off[4] = b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(content), content)

	if hdr == "" {
		hdr = fmt.Sprintf("/N %d /First %d", len(inner), first)
	}
	off[5] = b.Len()
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /ObjStm %s /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		hdr, len(objstm), objstm)

	off[6] = b.Len()
	var entries bytes.Buffer
	put := func(kind byte, f2 uint32, f3 uint16) {
		entries.WriteByte(kind)
		binary.Write(&entries, binary.BigEndian, f2)
		binary.Write(&entries, binary.BigEndian, f3)
	}
	put(0, 0, 65535)          // object 0, free
	put(2, 5, 0)              // object 1, in stream 5 at index 0
	put(2, 5, 1)              // object 2
	put(2, 5, 2)              // object 3
	put(1, uint32(off[4]), 0) // object 4, at a file offset
	put(1, uint32(off[5]), 0) // object 5
	put(1, uint32(off[6]), 0) // object 6
	fmt.Fprintf(&b, "6 0 obj\n<< /Type /XRef /Size 7 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		entries.Len(), entries.String())

	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[6])
	return []byte(b.String())
}

// TestObjectStreamStillResolves guards the object stream path: the /N loop now
// stops at the first non-integer pair instead of trusting the declared count,
// and offsets are range checked.
func TestObjectStreamStillResolves(t *testing.T) {
	data := objStmPDF()

	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if n := r.NumPage(); n != 1 {
		t.Fatalf("NumPage = %d, want 1", n)
	}

	// Reaching the page at all means objects 1, 2 and 3 were resolved out of
	// the object stream.
	p := r.Page(1)
	if p.V.IsNull() {
		t.Fatal("Page(1) is null")
	}
	if got := p.V.Key("Type").Name(); got != "Page" {
		t.Errorf("page /Type = %q, want %q", got, "Page")
	}
	text, err := p.GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(text, "Hello Stream") {
		t.Errorf("GetPlainText = %q, want it to contain %q", text, "Hello Stream")
	}
}

// TestMalformedObjectStream covers the object stream header. /N is a declared
// count that the pair loop no longer trusts, and /First and the per object
// offsets are file supplied positions used to seek.
func TestMalformedObjectStream(t *testing.T) {
	tests := []struct {
		name string
		hdr  string
	}{
		{"n huge", "/N 9223372036854775807 /First 14"},
		{"n negative", "/N -1 /First 14"},
		{"n zero", "/N 0 /First 14"},
		{"first zero", "/N 3 /First 0"},
		{"first negative", "/N 3 /First -1"},
		{"first huge", "/N 3 /First 9223372036854775807"},
		{"first missing", "/N 3"},
		{"n missing", "/First 14"},
		{"both missing", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr := tt.hdr
			if hdr == "" {
				hdr = " " // keep objStmPDFWith from filling in the valid header
			}
			data := objStmPDFWith(hdr)
			// Opening may succeed, since the object stream is only read when an
			// object inside it is resolved. Either way nothing may crash or
			// hang, and any failure has to arrive as an error.
			mustNotCrash(t, func() {
				r, err := NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Logf("NewReader reported: %v", err)
					return
				}
				if _, err := r.GetPlainText(); err != nil {
					t.Logf("GetPlainText reported: %v", err)
				}
			})
		})
	}
}

// TestConcurrentReads pins the property that made resolve carry its recursion
// depth as a parameter instead of on the Reader: an opened Reader is immutable,
// so several goroutines may read from it at once. Run under -race to be
// meaningful.
func TestConcurrentReads(t *testing.T) {
	data := objStmPDF()
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	const goroutines = 8
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				text, err := r.Page(1).GetPlainText(nil)
				if err != nil {
					errs <- err
					return
				}
				if !strings.Contains(text, "Hello Stream") {
					errs <- fmt.Errorf("got %q", text)
					return
				}
			}
			errs <- nil
		}()
	}
	for i := 0; i < goroutines; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent read: %v", err)
		}
	}
}

// TestValidCmapStillParses guards against the CMap operand check rejecting
// well-formed cmaps.
func TestValidCmapStillParses(t *testing.T) {
	const cm = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
2 begincodespacerange
<00> <ff>
<0000> <ffff>
endcodespacerange
2 beginbfchar
<41> <0041>
<42> <0042>
endbfchar
1 beginbfrange
<43> <45> <0043>
endbfrange
endcmap
end
end`

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a well-formed cmap")
	}
	if len(m.bfchar) != 2 {
		t.Errorf("bfchar = %d entries, want 2", len(m.bfchar))
	}
	if len(m.bfrange) != 1 {
		t.Errorf("bfrange = %d entries, want 1", len(m.bfrange))
	}
	if len(m.space[0]) != 1 || len(m.space[1]) != 1 {
		t.Errorf("codespace ranges = %d one-byte, %d two-byte, want 1 and 1",
			len(m.space[0]), len(m.space[1]))
	}
	if got := m.Decode("A"); got != "A" {
		t.Errorf("Decode(A) = %q, want %q", got, "A")
	}
	if got := m.Decode("D"); got != "D" {
		t.Errorf("Decode(D) = %q, want %q", got, "D")
	}
}
