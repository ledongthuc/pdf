package pdf

import (
	"bytes"
	"encoding/ascii85"
	"errors"
	"io"
	"testing"
)

var errReadPastEOD = errors.New("read past end of ASCII85 data")

type chunkReader struct {
	chunks [][]byte
	err    error
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, r.err
	}
	n := copy(p, r.chunks[0])
	if n < len(r.chunks[0]) {
		r.chunks[0] = r.chunks[0][n:]
	} else {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func encodeASCII85(t *testing.T, src []byte) []byte {
	t.Helper()
	dst := make([]byte, ascii85.MaxEncodedLen(len(src)))
	n := ascii85.Encode(dst, src)
	return dst[:n]
}

func decodeThroughAlphaReader(t *testing.T, chunks [][]byte, underlyingErr error) ([]byte, error) {
	t.Helper()
	r := newAlphaReader(&chunkReader{chunks: chunks, err: underlyingErr})
	return io.ReadAll(ascii85.NewDecoder(r))
}

func TestAlphaReaderPreservesZeroGroups(t *testing.T) {
	src := []byte("ABCD\x00\x00\x00\x00\x00\x00\x00\x00EFGH")
	encoded := encodeASCII85(t, src)
	if !bytes.Contains(encoded, []byte("z")) {
		t.Fatalf("test setup: encoded form %q must contain 'z' zero-group shorthand", encoded)
	}
	stream := append(encoded, "~>"...)

	got, err := decodeThroughAlphaReader(t, [][]byte{stream}, io.EOF)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("decoded %q, want %q", got, src)
	}
}

func TestAlphaReaderSignalsEOFAtMarker(t *testing.T) {
	src := []byte("payload that ends with an explicit EOD marker")
	encoded := encodeASCII85(t, src)
	half := len(encoded) / 2
	chunks := [][]byte{encoded[:half], append(append([]byte{}, encoded[half:]...), "~>"...)}

	got, err := decodeThroughAlphaReader(t, chunks, errReadPastEOD)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("decoded %q, want %q", got, src)
	}
}

func TestAlphaReaderMarkerSplitAcrossReads(t *testing.T) {
	src := []byte("marker split between two reads")
	encoded := encodeASCII85(t, src)
	chunks := [][]byte{append(append([]byte{}, encoded...), '~'), []byte(">")}

	got, err := decodeThroughAlphaReader(t, chunks, errReadPastEOD)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("decoded %q, want %q", got, src)
	}
}

func TestAlphaReaderIgnoresDataAfterMarker(t *testing.T) {
	src := []byte("only bytes before the marker count")
	encoded := encodeASCII85(t, src)
	stream := append(encoded, "~>\nendstream\nendobj\n"...)
	chunks := [][]byte{stream, []byte("4 0 obj <</Length 99>>")}

	got, err := decodeThroughAlphaReader(t, chunks, errReadPastEOD)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("decoded %q, want %q", got, src)
	}
}

func TestAlphaReaderKeepsWhitespaceHarmless(t *testing.T) {
	src := []byte("data wrapped across several lines, as PDF generators do")
	encoded := encodeASCII85(t, src)
	var wrapped []byte
	for i, c := range encoded {
		if i > 0 && i%20 == 0 {
			wrapped = append(wrapped, '\r', '\n')
		}
		wrapped = append(wrapped, c)
	}
	wrapped = append(wrapped, "\n~>\n"...)

	got, err := decodeThroughAlphaReader(t, [][]byte{wrapped}, errReadPastEOD)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("decoded %q, want %q", got, src)
	}
}

func TestAlphaReaderReadAfterEOF(t *testing.T) {
	stream := append(encodeASCII85(t, []byte("tail")), "~>"...)
	r := newAlphaReader(&chunkReader{chunks: [][]byte{stream}, err: errReadPastEOD})
	if _, err := io.ReadAll(ascii85.NewDecoder(r)); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	buf := make([]byte, 16)
	for i := 0; i < 3; i++ {
		n, err := r.Read(buf)
		if n != 0 || err != io.EOF {
			t.Fatalf("read #%d after EOD: got (%d, %v), want (0, io.EOF)", i+1, n, err)
		}
	}
}
