package pdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"io"
	"strings"
	"testing"
)

func TestValueReaderOnNonStream(t *testing.T) {
	v := testValue(int64(1))
	rd := v.Reader()
	data, err := io.ReadAll(rd)
	if err == nil {
		t.Fatal("expected error reading non-stream value")
	}
	if !strings.Contains(err.Error(), "stream not present") {
		t.Fatalf("error = %q, want it to mention 'stream not present'", err)
	}
	if len(data) != 0 {
		t.Fatalf("read %q, want empty", data)
	}
}

func TestPNGUpReader(t *testing.T) {
	// Two rows of width 2 encoded with the PNG "Up" predictor (filter byte 2).
	// Row 1: raw [5, 10]; Row 2: raw [1, 1] (differences from the previous row).
	input := []byte{2, 5, 10, 2, 1, 1}
	r := &pngUpReader{r: bytes.NewReader(input), hist: make([]byte, 3), tmp: make([]byte, 3)}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	// hist accumulates: after row1 [5,10]; after row2 [5+1, 10+1] = [6,11].
	want := []byte{5, 10, 6, 11}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded = %v, want %v", got, want)
	}
}

func TestPNGUpReaderRejectsBadFilter(t *testing.T) {
	input := []byte{1, 5, 10} // filter byte 1 is not "Up"
	r := &pngUpReader{r: bytes.NewReader(input), hist: make([]byte, 3), tmp: make([]byte, 3)}
	_, err := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "malformed PNG-Up") {
		t.Fatalf("err = %v, want malformed PNG-Up error", err)
	}
}

func TestCBCReader(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{0x05}, 16)
	plaintext := []byte("abcdefghijklmnopabcdefghijklmnop") // 32 bytes (2 blocks)
	ct := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, plaintext)

	r := &cbcReader{
		cbc: cipher.NewCBCDecrypter(block, iv),
		rd:  bytes.NewReader(ct),
		buf: make([]byte, 16),
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestCryptKeyDeterministicAndSaltAware(t *testing.T) {
	k1 := cryptKey([]byte("secret"), false, objptr{id: 1, gen: 0})
	k2 := cryptKey([]byte("secret"), false, objptr{id: 1, gen: 0})
	if !bytes.Equal(k1, k2) {
		t.Fatal("cryptKey is not deterministic")
	}
	if k3 := cryptKey([]byte("secret"), true, objptr{id: 1, gen: 0}); bytes.Equal(k1, k3) {
		t.Fatal("cryptKey should differ when useAES (salt) differs")
	}
	if k4 := cryptKey([]byte("secret"), false, objptr{id: 2, gen: 0}); bytes.Equal(k1, k4) {
		t.Fatal("cryptKey should differ when object id differs")
	}
	if len(k1) != 16 {
		t.Fatalf("md5-based key length = %d, want 16", len(k1))
	}
}

func TestDecryptStringRC4RoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef")
	ptr := objptr{id: 3, gen: 1}
	plaintext := "hello, encrypted world"

	// RC4 is a stream cipher; applying the same keystream twice recovers the
	// plaintext, so decryptString is its own inverse here.
	enc := decryptString(key, false, ptr, plaintext)
	if enc == plaintext {
		t.Fatal("RC4 pass did not transform the plaintext")
	}
	if got := decryptString(key, false, ptr, enc); got != plaintext {
		t.Fatalf("round trip = %q, want %q", got, plaintext)
	}
}

func TestDecryptStringAESRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 16)
	ptr := objptr{id: 7, gen: 2}

	derived := cryptKey(key, true, ptr)
	block, err := aes.NewCipher(derived)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{0x01}, 16)
	plaintext := []byte("0123456789abcdef") // exactly one block, no padding
	ct := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, plaintext)

	sealed := string(append(append([]byte{}, iv...), ct...))
	if got := decryptString(key, true, ptr, sealed); got != string(plaintext) {
		t.Fatalf("round trip = %q, want %q", got, plaintext)
	}
}

func TestOkayV4(t *testing.T) {
	valid := dict{
		name("CF"): dict{
			name("StdCF"): dict{
				name("CFM"):       name("AESV2"),
				name("Length"):    int64(16),
				name("AuthEvent"): name("DocOpen"),
			},
		},
		name("StmF"): name("StdCF"),
		name("StrF"): name("StdCF"),
	}
	if !okayV4(valid) {
		t.Fatal("okayV4(valid) = false, want true")
	}

	if okayV4(dict{name("StmF"): name("A"), name("StrF"): name("B")}) {
		t.Fatal("okayV4 should reject StmF != StrF")
	}
	badCFM := dict{
		name("CF"):   dict{name("StdCF"): dict{name("CFM"): name("V2"), name("Length"): int64(16)}},
		name("StmF"): name("StdCF"),
		name("StrF"): name("StdCF"),
	}
	if okayV4(badCFM) {
		t.Fatal("okayV4 should reject CFM != AESV2")
	}
	badLength := dict{
		name("CF"):   dict{name("StdCF"): dict{name("CFM"): name("AESV2"), name("Length"): int64(8)}},
		name("StmF"): name("StdCF"),
		name("StrF"): name("StdCF"),
	}
	if okayV4(badLength) {
		t.Fatal("okayV4 should reject non-16-byte key length")
	}
}
