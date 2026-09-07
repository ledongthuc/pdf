package pdf

import (
	"testing"
)

func TestUcs2Encoder(t *testing.T) {
	// raw content in PDF (hexed for pretty)
	// 6c5f82cf94f6884c005c284ea4661362636b3e56de5355005c29

	// raw bytes in showEncodedText (hexed for pretty)
	// 6c5f82cf94f6884c00284ea4661362636b3e56de53550029
	data := []byte{
		0x6C, 0x5F, // 江
		0x82, 0xCF, // 苏
		0x94, 0xF6, // 银
		0x88, 0x4C, // 行
		0x00, 0x28, // (
		0x4E, 0xA4, // 交
		0x66, 0x13, // 易
		0x62, 0x63, // 扣
		0x6B, 0x3E, // 款
		0x56, 0xDE, // 回
		0x53, 0x55, // 单
		0x00, 0x29, // )
	}

	var e ucs2Encoder
	text := e.Decode(string(data))

	const want = "江苏银行(交易扣款回单)"
	if text != want {
		t.Errorf("got %q, want %q", text, want)
	"reflect"
	"sort"
	"testing"
)

func TestNopEncoder(t *testing.T) {
	e := &nopEncoder{}
	if got := e.Decode("abc"); got != "abc" {
		t.Fatalf("Decode = %q, want %q", got, "abc")
	}
}

func TestByteEncoderWinAnsi(t *testing.T) {
	e := &byteEncoder{&winAnsiEncoding}
	if got := e.Decode("A"); got != "A" {
		t.Fatalf("Decode(A) = %q, want %q", got, "A")
	}
	if got := e.Decode("\x80"); got != "\u20ac" {
		t.Fatalf("Decode(0x80) = %q, want %q (euro)", got, "\u20ac")
	}
}

func TestByteEncoderMacRoman(t *testing.T) {
	e := &byteEncoder{&macRomanEncoding}
	if got := e.Decode("A"); got != "A" {
		t.Fatalf("Decode(A) = %q, want %q", got, "A")
	}
	// 0x80 is Ä in MacRomanEncoding.
	if got := e.Decode("\x80"); got != "\u00c4" {
		t.Fatalf("Decode(0x80) = %q, want %q", got, "\u00c4")
	}
}

func TestDictEncoder(t *testing.T) {
	// Differences array: code 65 -> /Alpha (0x0391).
	e := &dictEncoder{v: testValue(array{int64(65), name("Alpha")})}
	if got := e.Decode("A"); got != "\u0391" {
		t.Fatalf("Decode(A) = %q, want %q", got, "\u0391")
	}

	// Unknown glyph name falls back to the raw byte.
	e2 := &dictEncoder{v: testValue(array{int64(65), name("NotAGlyphName")})}
	if got := e2.Decode("A"); got != "A" {
		t.Fatalf("Decode(A) = %q, want %q (fallback)", got, "A")
	}
}

func TestCmapDecodeBFChar(t *testing.T) {
	m := &cmap{
		space:  [4][]byteRange{},
		bfchar: []bfchar{{orig: "\x00\x01", repl: "\x00A"}}, // 0x0001 -> UTF-16BE "A"
	}
	m.space[1] = []byteRange{{low: "\x00\x00", high: "\xff\xff"}}

	if got := m.Decode("\x00\x01\x00\x01"); got != "AA" {
		t.Fatalf("Decode = %q, want %q", got, "AA")
	}
}

func TestMatrixMul(t *testing.T) {
	m := matrix{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}
	if got := ident.mul(m); got != m {
		t.Fatalf("ident.mul(m) = %v, want %v", got, m)
	}
	if got := m.mul(ident); got != m {
		t.Fatalf("m.mul(ident) = %v, want %v", got, m)
	}

	a := matrix{{1, 0, 0}, {0, 1, 0}, {2, 3, 1}}
	b := matrix{{1, 0, 0}, {0, 1, 0}, {5, 7, 1}}
	want := matrix{{1, 0, 0}, {0, 1, 0}, {7, 10, 1}}
	if got := a.mul(b); got != want {
		t.Fatalf("a.mul(b) = %v, want %v", got, want)
	}
}

func TestTextVerticalSort(t *testing.T) {
	x := TextVertical{
		{Y: 10, X: 5, S: "a"},
		{Y: 20, X: 1, S: "b"},
		{Y: 10, X: 2, S: "c"},
	}
	sort.Sort(x)
	var got []string
	for _, t := range x {
		got = append(got, t.S)
	}
	want := []string{"b", "c", "a"} // topmost Y first, then leftmost X
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted order = %v, want %v", got, want)
	}
}

func TestTextHorizontalSort(t *testing.T) {
	x := TextHorizontal{
		{X: 5, Y: 10, S: "a"},
		{X: 1, Y: 20, S: "b"},
		{X: 5, Y: 30, S: "c"},
	}
	sort.Sort(x)
	var got []string
	for _, t := range x {
		got = append(got, t.S)
	}
	want := []string{"b", "c", "a"} // leftmost X first, then topmost Y
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted order = %v, want %v", got, want)
	}
}

func TestFontAccessors(t *testing.T) {
	f := Font{V: testValue(dict{
		name("BaseFont"):  name("Helvetica"),
		name("FirstChar"): int64(32),
		name("LastChar"):  int64(33),
		name("Widths"):    array{int64(250), int64(500)},
	})}

	if f.BaseFont() != "Helvetica" {
		t.Fatalf("BaseFont() = %q, want Helvetica", f.BaseFont())
	}
	if f.FirstChar() != 32 || f.LastChar() != 33 {
		t.Fatalf("FirstChar/LastChar = %d/%d, want 32/33", f.FirstChar(), f.LastChar())
	}
	want := []float64{250, 500}
	if got := f.Widths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Widths() = %v, want %v", got, want)
	}
	if f.Width(32) != 250 || f.Width(33) != 500 {
		t.Fatal("Width() returned wrong value for in-range code")
	}
	if f.Width(31) != 0 || f.Width(34) != 0 {
		t.Fatal("Width() should return 0 for out-of-range code")
	}
}

func TestFontEncoderCaches(t *testing.T) {
	f := &Font{V: testValue(dict{name("Encoding"): name("WinAnsiEncoding")})}
	e1 := f.Encoder()
	e2 := f.Encoder()
	if f.enc == nil {
		t.Fatal("encoding was not cached on the Font")
	}
	if e1 != e2 {
		t.Fatal("Encoder() returned different instances; caching is broken")
	}
}

func TestBuildOutline(t *testing.T) {
	entry := testValue(dict{
		name("Title"): "Root",
		name("First"): dict{
			name("Title"): "Chapter 1",
			name("Next"): dict{
				name("Title"): "Chapter 2",
			},
		},
	})

	got := buildOutline(entry)
	want := Outline{
		Title: "Root",
		Child: []Outline{
			{Title: "Chapter 1"},
			{Title: "Chapter 2"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildOutline = %+v, want %+v", got, want)
	}
}
