// redteam_test.go — hostile-input probes for the PDF attack surface.
//
// Probe map (§7 of the security-audit skill, redteam mode):
//   P1  stream Length > file size — graceful EOF, no hang
//   P2  truncated xref table — error returned, no panic
//   P3  object depth > maxObjectDepth at OpenBytes level — error, no panic
//   P4a malformed CMap: endbfchar without beginbfchar — no panic from readCmap
//   P4b malformed CMap: endbfrange without beginbfrange — no panic from readCmap
//   P5  FlateDecode stream followed by trailing garbage — no panic, readable
package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// buildXrefPDF returns a minimal but structurally valid PDF where the xref
// table sits immediately after the 9-byte "%PDF-1.4\n" header (startxref=9),
// and the trailer dictionary is replaced by the caller-supplied string.
func buildXrefPDF(trailer string) []byte {
	s := "%PDF-1.4\n" +
		"xref\n" +
		"0 1\n" +
		"0000000000 65535 f\n" +
		"trailer\n" +
		trailer +
		"\nstartxref\n9\n%%EOF"
	return []byte(s)
}

// P1 — Stream Length claims more bytes than the underlying file contains.
// Reader() must return EOF (or an error) without hanging the goroutine.
func TestRedTeamStreamLengthOverflow(t *testing.T) {
	actual := []byte("hello")
	r := &Reader{f: bytes.NewReader(actual), end: int64(len(actual))}
	// stream header claims Length=999999 but the file only has 5 bytes.
	s := stream{hdr: dict{name("Length"): int64(999999)}, offset: 0}
	v := Value{r, objptr{}, s}

	done := make(chan struct{}, 1)
	go func() {
		defer func() {
			recover()
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		rd := v.Reader()
		io.Copy(io.Discard, rd) //nolint
		done <- struct{}{}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("P1: Reader hung on stream with inflated Length field")
	}
}

// P2 — Truncated xref table: PDF ends before any object-offset entries.
// OpenBytes must return an error, not panic.
func TestRedTeamTruncatedXref(t *testing.T) {
	pdf := []byte("%PDF-1.4\nstartxref\n0\n%%EOF")
	_, err := OpenBytes(pdf)
	if err == nil {
		t.Fatal("P2: expected error on truncated xref, got nil")
	}
}

// P3 — Object depth exceeds maxObjectDepth inside the PDF trailer dict.
// NewReaderEncrypted's defer-recover() must catch the panic and return an error.
func TestRedTeamObjectDepthExceedsMax(t *testing.T) {
	var nested strings.Builder
	for i := 0; i < maxObjectDepth+100; i++ {
		nested.WriteString("<</A ")
	}
	nested.WriteString("/Size 1 /Root 1 0 R")
	for i := 0; i < maxObjectDepth+100; i++ {
		nested.WriteString(" >>")
	}

	pdf := buildXrefPDF(nested.String())
	_, err := OpenBytes(pdf)
	if err == nil {
		t.Fatal("P3: expected error on over-depth PDF trailer, got nil")
	}
}

// P4a — Malformed CMap: endbfchar appears without a preceding beginbfchar.
// cmap.go:handleEndBfchar currently uses panic("missing beginbfchar") which
// escapes both Interpret() and readCmap() uncaught.
// Expect: no panic propagates beyond readCmap.
func TestRedTeamMalformedCMapEndbfcharNoPreceding(t *testing.T) {
	data := []byte("begincmap\nendbfchar\nendcmap\n")
	v := testStream(data)

	panicCh := make(chan interface{}, 1)
	go func() {
		var pv interface{}
		func() {
			defer func() { pv = recover() }()
			readCmap(v)
		}()
		panicCh <- pv
	}()

	select {
	case pv := <-panicCh:
		if pv != nil {
			t.Fatalf("P4a: readCmap panicked on malformed CMap (endbfchar without beginbfchar): %v\n"+
				"Fix: replace panic(string) in handleEndBfchar with s.ok=false; return", pv)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("P4a: readCmap hung on malformed CMap")
	}
}

// P4b — Malformed CMap: endbfrange appears without a preceding beginbfrange.
// cmap.go:handleEndBfrange currently uses panic("missing beginbfrange") which
// escapes both Interpret() and readCmap() uncaught.
// Expect: no panic propagates beyond readCmap.
func TestRedTeamMalformedCMapEndbfrangeNoPreceding(t *testing.T) {
	data := []byte("begincmap\nendbfrange\nendcmap\n")
	v := testStream(data)

	panicCh := make(chan interface{}, 1)
	go func() {
		var pv interface{}
		func() {
			defer func() { pv = recover() }()
			readCmap(v)
		}()
		panicCh <- pv
	}()

	select {
	case pv := <-panicCh:
		if pv != nil {
			t.Fatalf("P4b: readCmap panicked on malformed CMap (endbfrange without beginbfrange): %v\n"+
				"Fix: replace panic(string) in handleEndBfrange with s.ok=false; return", pv)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("P4b: readCmap hung on malformed CMap")
	}
}

// P4c — Malformed CMap: beginbfchar count is 2 billion, causing a hang loop.
// interpretCmapRanges must reject counts outside [0, maxCmapEntries].
// Expect: readCmap returns within the timeout; no hang, no panic.
func TestRedTeamMalformedCMapHugeCount(t *testing.T) {
	data := []byte("begincmap\n2000000000 beginbfchar\nendbfchar\nendcmap\n")
	v := testStream(data)

	done := make(chan interface{}, 1)
	go func() {
		var pv interface{}
		func() {
			defer func() { pv = recover() }()
			readCmap(v)
		}()
		done <- pv
	}()

	select {
	case pv := <-done:
		if pv != nil {
			t.Fatalf("P4c: readCmap panicked on huge beginbfchar count: %v", pv)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("P4c: readCmap hung on huge beginbfchar count (resource exhaustion)")
	}
}

// P6 — xref stream Size field exceeds maxXrefObjects.
// readXrefStream must reject the oversized allocation before make().
// Expect: OpenBytes returns an error within the timeout; no hang or crash.
func TestRedTeamXrefSizeOverflow(t *testing.T) {
	// Build a minimal PDF with an xref stream claiming Size=1e9.
	// The xref stream itself uses the simplest valid compressed cross-reference
	// format (W=[1 1 1], one free entry).  The hostile field is /Size.
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zw.Write([]byte{0, 0, 0, 0}) // one type-0 (free) entry: type=0, off=0, gen=0
	zw.Close()
	xrefStream := compressed.Bytes()

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	xrefOff := int64(buf.Len())

	fmt.Fprintf(&buf, "1 0 obj\n")
	fmt.Fprintf(&buf, "<< /Type /XRef /Size 1000000000 /W [1 1 1] /Length %d >>\n", len(xrefStream))
	buf.WriteString("stream\n")
	buf.Write(xrefStream)
	buf.WriteString("\nendstream\nendobj\n")
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOff)

	_, err := OpenBytes(buf.Bytes())
	if err == nil || !strings.Contains(err.Error(), "Size out of range") {
		t.Fatalf("P6: expected 'Size out of range' error, got: %v\n"+
			"(if nil: fixture may error before reaching the guard; fix the fixture)\n"+
			"(if wrong message: guard fired but message changed)", err)
	}
}

// P5 — FlateDecode stream with trailing garbage bytes appended after valid zlib data.
// The io.LimitReader cap in applyFilter means trailing bytes are never read.
// Expect: no panic; the filtered reader yields the original content.
func TestRedTeamFlateTrailingGarbage(t *testing.T) {
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zw.Write([]byte("hello world")) //nolint:errcheck
	zw.Close()
	withGarbage := append(compressed.Bytes(), 0xFF, 0xFE, 0x00, 0xAB, 0xCD, 0xEF)

	var pv interface{}
	func() {
		defer func() { pv = recover() }()
		rd, err := applyFilter(bytes.NewReader(withGarbage), "FlateDecode", Value{})
		if err != nil {
			return
		}
		io.Copy(io.Discard, rd) //nolint:errcheck
	}()
	if pv != nil {
		t.Fatalf("P5: FlateDecode with trailing garbage panicked: %v", pv)
	}
}
