package pdf

import (
	"bytes"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// _XrefStreamSizeOverAlloc demonstrates an unbounded allocation driven by the
// attacker-controlled /Size entry of a cross-reference stream. readXrefStream
// does `table := make([]xref, size)` with no upper bound on size, so a ~130-byte
// PDF forces an allocation of size * sizeof(xref) bytes (sizeof(xref) == 32).
//
// We set /Size to 50,000,000, which forces a ~1.6 GB allocation from a tiny
// input. We omit /W so that readXrefStreamData returns an error immediately
// AFTER the make() (so the rest of the parse does no further work). The test
// asserts that parsing a tiny file stays within a small memory budget; it FAILS
// on current code because the make() has already allocated ~1.6 GB.
func TestXrefStreamSizeOverAlloc(t *testing.T) {
	const bigSize = 50_000_000 // * 32 bytes/xref ≈ 1.6 GB

	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	xrefOff := b.Len() // offset where the xref-stream object begins
	b.WriteString("1 0 obj\n")
	b.WriteString("<</Type /XRef /Size " + strconv.Itoa(bigSize) + ">>\n")
	b.WriteString("stream\n")
	// filler so the whole file comfortably exceeds the 100-byte tail window
	b.WriteString(strings.Repeat("%", 60) + "\n")
	b.WriteString("startxref\n" + strconv.Itoa(xrefOff) + "\n%%EOF\n")

	data := b.Bytes()
	ra := bytes.NewReader(data)

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)

	// returns an error (missing /W), but only AFTER make([]xref, bigSize)
	_, _ = NewReader(ra, int64(len(data)))

	runtime.ReadMemStats(&m1)
	allocMB := float64(m1.TotalAlloc-m0.TotalAlloc) / (1024 * 1024)

	t.Logf("input size = %d bytes, allocated during parse = %.1f MB", len(data), allocMB)

	const budgetMB = 64
	if allocMB > budgetMB {
		t.Fatalf("parsing a %d-byte PDF allocated %.1f MB (> %d MB budget): "+
			"/Size (%d) flows unbounded into make([]xref, size)",
			len(data), allocMB, budgetMB, bigSize)
	}
}
