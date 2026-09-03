package pdf

import (
	"reflect"
	"testing"
)

// testValue builds a Value with an explicit underlying object, independent of
// any real Reader/xref resolution. r is non-nil only so that Key/Index can
// call resolve without a nil-receiver problem when the value is a direct
// (non-reference) object.
func testValue(data interface{}) Value {
	return Value{r: &Reader{}, ptr: objptr{}, data: data}
}

func TestValueZeroValueIsNull(t *testing.T) {
	var v Value
	if !v.IsNull() {
		t.Fatal("zero Value should report IsNull")
	}
	if v.Kind() != Null {
		t.Fatalf("Kind() = %v, want Null", v.Kind())
	}
	if v.Bool() {
		t.Fatal("Bool() = true, want false")
	}
	if v.Int64() != 0 {
		t.Fatalf("Int64() = %d, want 0", v.Int64())
	}
	if v.Float64() != 0 {
		t.Fatalf("Float64() = %v, want 0", v.Float64())
	}
	if v.RawString() != "" {
		t.Fatalf("RawString() = %q, want empty", v.RawString())
	}
	if v.Text() != "" {
		t.Fatalf("Text() = %q, want empty", v.Text())
	}
	if v.Name() != "" {
		t.Fatalf("Name() = %q, want empty", v.Name())
	}
	if v.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", v.Len())
	}
	if v.Index(0).Kind() != Null {
		t.Fatalf("Index(0).Kind() = %v, want Null", v.Index(0).Kind())
	}
	if v.Key("Anything").Kind() != Null {
		t.Fatalf("Key().Kind() = %v, want Null", v.Key("Anything").Kind())
	}
	if keys := v.Keys(); keys != nil {
		t.Fatalf("Keys() = %v, want nil", keys)
	}
	if got := v.String(); got != "<nil>" {
		t.Fatalf("String() = %q, want %q", got, "<nil>")
	}
}

func TestValueKindAndAccessors(t *testing.T) {
	cases := []struct {
		name     string
		data     interface{}
		kind     ValueKind
		asInt    int64
		asFloat  float64
		asBool   bool
		asString string
	}{
		{"bool", true, Bool, 0, 0, true, ""},
		{"int", int64(42), Integer, 42, 42, false, ""},
		{"real", float64(3.5), Real, 0, 3.5, false, ""},
		{"name", name("Helvetica"), Name, 0, 0, false, ""},
		{"string", "hello", String, 0, 0, false, "hello"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := testValue(tc.data)
			if v.Kind() != tc.kind {
				t.Fatalf("Kind() = %v, want %v", v.Kind(), tc.kind)
			}
			if v.Int64() != tc.asInt {
				t.Fatalf("Int64() = %d, want %d", v.Int64(), tc.asInt)
			}
			if v.Float64() != tc.asFloat {
				t.Fatalf("Float64() = %v, want %v", v.Float64(), tc.asFloat)
			}
			if v.Bool() != tc.asBool {
				t.Fatalf("Bool() = %v, want %v", v.Bool(), tc.asBool)
			}
			if v.RawString() != tc.asString {
				t.Fatalf("RawString() = %q, want %q", v.RawString(), tc.asString)
			}
		})
	}
}

func TestValueNameAccessorStripsSlash(t *testing.T) {
	v := testValue(name("Helvetica"))
	if v.Name() != "Helvetica" {
		t.Fatalf("Name() = %q, want %q", v.Name(), "Helvetica")
	}
}

func TestValueArrayAccessors(t *testing.T) {
	v := testValue(array{int64(1), name("two"), float64(3.5)})
	if v.Kind() != Array {
		t.Fatalf("Kind() = %v, want Array", v.Kind())
	}
	if v.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", v.Len())
	}
	if v.Index(0).Int64() != 1 {
		t.Fatalf("Index(0).Int64() = %d, want 1", v.Index(0).Int64())
	}
	if v.Index(1).Name() != "two" {
		t.Fatalf("Index(1).Name() = %q, want %q", v.Index(1).Name(), "two")
	}
	if v.Index(2).Float64() != 3.5 {
		t.Fatalf("Index(2).Float64() = %v, want 3.5", v.Index(2).Float64())
	}
	if v.Index(-1).Kind() != Null || v.Index(3).Kind() != Null {
		t.Fatal("out-of-bounds Index should return null Value")
	}
}

func TestValueDictAccessors(t *testing.T) {
	v := testValue(dict{name("B"): int64(2), name("A"): int64(1)})
	if v.Kind() != Dict {
		t.Fatalf("Kind() = %v, want Dict", v.Kind())
	}
	keys := v.Keys()
	if !reflect.DeepEqual(keys, []string{"A", "B"}) {
		t.Fatalf("Keys() = %v, want [A B] (sorted)", keys)
	}
	if v.Key("A").Int64() != 1 || v.Key("B").Int64() != 2 {
		t.Fatal("Key() returned wrong value")
	}
	if v.Key("Missing").Kind() != Null {
		t.Fatal("Key() for missing key should be Null")
	}
}

func TestValueTextDecoding(t *testing.T) {
	// Plain ASCII.
	if got := testValue("hello").Text(); got != "hello" {
		t.Fatalf("Text() = %q, want %q", got, "hello")
	}
	// UTF-16BE with BOM.
	if got := testValue("\xfe\xff\x00A\x00B").Text(); got != "AB" {
		t.Fatalf("Text() = %q, want %q", got, "AB")
	}
	// TextFromUTF16 assumes raw big-endian UTF-16 without BOM.
	if got := testValue("\x00A").TextFromUTF16(); got != "A" {
		t.Fatalf("TextFromUTF16() = %q, want %q", got, "A")
	}
	if got := testValue("A").TextFromUTF16(); got != "" {
		t.Fatalf("TextFromUTF16(odd length) = %q, want empty", got)
	}
	if got := testValue("").TextFromUTF16(); got != "" {
		t.Fatalf("TextFromUTF16(empty) = %q, want empty", got)
	}
}

func TestIsInteger(t *testing.T) {
	cases := map[string]bool{
		"":     false,
		"+":    false,
		"-":    false,
		"0":    true,
		"123":  true,
		"-45":  true,
		"+0":   true,
		"12.3": false,
		"1a":   false,
		"--1":  false,
		"1 2":  false,
	}
	for in, want := range cases {
		if got := isInteger(in); got != want {
			t.Errorf("isInteger(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsReal(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"1":     false,
		"1.5":   true,
		".5":    true,
		"1.":    true,
		"-1.5":  true,
		"+0.0":  true,
		"1.2.3": false,
		"abc":   false,
	}
	for in, want := range cases {
		if got := isReal(in); got != want {
			t.Errorf("isReal(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsSpace(t *testing.T) {
	for _, b := range []byte{'\x00', '\t', '\n', '\f', '\r', ' '} {
		if !isSpace(b) {
			t.Errorf("isSpace(%q) = false, want true", b)
		}
	}
	for _, b := range []byte{'a', '1', '<', '/', 0x80} {
		if isSpace(b) {
			t.Errorf("isSpace(%q) = true, want false", b)
		}
	}
}

func TestIsDelim(t *testing.T) {
	for _, b := range []byte{'<', '>', '(', ')', '[', ']', '{', '}', '/', '%'} {
		if !isDelim(b) {
			t.Errorf("isDelim(%q) = false, want true", b)
		}
	}
	for _, b := range []byte{'a', '1', ' ', '\n'} {
		if isDelim(b) {
			t.Errorf("isDelim(%q) = true, want false", b)
		}
	}
}

func TestUnhex(t *testing.T) {
	cases := map[byte]int{
		'0': 0, '9': 9,
		'a': 10, 'f': 15,
		'A': 10, 'F': 15,
		'g': -1, 'z': -1, ' ': -1,
	}
	for in, want := range cases {
		if got := unhex(in); got != want {
			t.Errorf("unhex(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestDecodeInt(t *testing.T) {
	cases := []struct {
		in   []byte
		want int
	}{
		{nil, 0},
		{[]byte{0x00}, 0},
		{[]byte{0x01, 0x02}, 0x0102},
		{[]byte{0xff}, 255},
		{[]byte{0x12, 0x34, 0x56}, 0x123456},
	}
	for _, tc := range cases {
		if got := decodeInt(tc.in); got != tc.want {
			t.Errorf("decodeInt(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFindLastLine(t *testing.T) {
	buf := []byte("abc\nstartxref\n123\n%%EOF\n")
	if got := findLastLine(buf, "startxref"); got != 4 {
		t.Fatalf("findLastLine = %d, want 4", got)
	}
	if got := findLastLine(buf, "missing"); got != -1 {
		t.Fatalf("findLastLine(missing) = %d, want -1", got)
	}
	// "startxref" at position 0 or with no trailing newline is rejected.
	if got := findLastLine([]byte("startxref"), "startxref"); got != -1 {
		t.Fatalf("findLastLine(bare) = %d, want -1", got)
	}
}

func TestUTF16Helpers(t *testing.T) {
	if !isUTF16("\xfe\xff\x00A") {
		t.Fatal("isUTF16 should detect BOM-prefixed even-length string")
	}
	if isUTF16("\xfe\xffA") {
		t.Fatal("isUTF16 should reject odd-length string")
	}
	if isUTF16("A") {
		t.Fatal("isUTF16 should reject non-BOM string")
	}
	if got := utf16Decode("\x00A\x00B"); got != "AB" {
		t.Fatalf("utf16Decode = %q, want %q", got, "AB")
	}
	if got := utf16Decode(""); got != "" {
		t.Fatalf("utf16Decode(empty) = %q, want empty", got)
	}
}

func TestPDFDocEncodedHelpers(t *testing.T) {
	if !isPDFDocEncoded("hello") {
		t.Fatal("isPDFDocEncoded(ASCII) = false, want true")
	}
	if isPDFDocEncoded("\x00") {
		t.Fatal("isPDFDocEncoded(0x00) = true, want false (unmapped byte)")
	}
	if !isPDFDocEncoded("\x80") {
		t.Fatal("isPDFDocEncoded(0x80) = false, want true (maps to bullet)")
	}
	if got := pdfDocDecode("hello"); got != "hello" {
		t.Fatalf("pdfDocDecode(ASCII) = %q, want %q", got, "hello")
	}
	if got := pdfDocDecode("\x80"); got != "\u2022" {
		t.Fatalf("pdfDocDecode(0x80) = %q, want %q", got, "\u2022")
	}
	if got := pdfDocDecode("a\x80"); got != "a\u2022" {
		t.Fatalf("pdfDocDecode(mixed) = %q, want %q", got, "a\u2022")
	}
}

func TestIsSameSentence(t *testing.T) {
	base := Text{Font: "Arial", FontSize: 10, X: 0, Y: 100, S: "hello"}
	same := Text{Font: "Arial", FontSize: 10, X: 50, Y: 102, S: "world"}
	if !IsSameSentence(base, same) {
		t.Fatal("expected same sentence for matching font/size and nearby Y")
	}

	if IsSameSentence(base, Text{Font: "Times", FontSize: 10, Y: 100, S: "x"}) {
		t.Fatal("different font should not be same sentence")
	}
	if IsSameSentence(base, Text{Font: "Arial", FontSize: 10.2, Y: 100, S: "x"}) {
		t.Fatal("different font size should not be same sentence")
	}
	if IsSameSentence(base, Text{Font: "Arial", FontSize: 10, Y: 106, S: "x"}) {
		t.Fatal("Y distance >= 5 should not be same sentence")
	}
	if IsSameSentence(Text{Font: "Arial", FontSize: 10, Y: 100, S: ""}, same) {
		t.Fatal("empty last.S should not be same sentence")
	}
}
