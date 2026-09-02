package pdf

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestReadASCII85StreamWithZeroGroup(t *testing.T) {
	f, r, err := Open("testdata/ascii85_zero_group.pdf")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	contents := r.Page(1).V.Key("Contents")
	decoded, err := io.ReadAll(contents.Reader())
	if err != nil {
		t.Fatalf("read content stream: %v", err)
	}
	want := []byte("BT /F1 12 Tf 72 720 Td (Hello ASCII85 A\x00\x00\x00\x00\x00\x00\x00\x00B) Tj ET")
	if !bytes.Equal(decoded, want) {
		t.Fatalf("decoded content stream:\n%q\nwant:\n%q", decoded, want)
	}
}

func TestReadASCII85FlateChainedStream(t *testing.T) {
	f, r, err := Open("testdata/ascii85_flate_chain.pdf")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	reader, err := r.GetPlainText()
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	text, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	if !strings.Contains(string(text), "Hello chained filters") {
		t.Fatalf("extracted text %q, want it to contain %q", text, "Hello chained filters")
	}
}
