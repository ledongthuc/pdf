package pdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"fmt"
	"io"
)

var passwordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// tryDecrypt handles the optional encryption step.  It tries the empty
// password first, then each password returned by pw in turn.  It returns nil
// when the file is unencrypted or decryption succeeds.
func tryDecrypt(r *Reader, pw func() string) error {
	if r.trailer["Encrypt"] == nil {
		return nil
	}
	err := r.initEncrypt("")
	if err == nil {
		return nil
	}
	if pw == nil || err != ErrInvalidPassword {
		return err
	}
	for {
		next := pw()
		if next == "" {
			break
		}
		if r.initEncrypt(next) == nil {
			return nil
		}
	}
	return err
}

// parseEncryptBody extracts and validates n, O, U, P, and ID from the
// Standard encryption dictionary and the file trailer.
func parseEncryptBody(encrypt dict, trailer dict) (n int64, O, U string, P uint32, ID []byte, err error) {
	if encrypt["Filter"] != name("Standard") {
		return 0, "", "", 0, nil, fmt.Errorf("unsupported PDF: encryption filter %v", objfmt(encrypt["Filter"]))
	}
	n, err = validateKeyLength(encrypt)
	if err != nil {
		return 0, "", "", 0, nil, err
	}
	ID, err = extractTrailerID(trailer)
	if err != nil {
		return 0, "", "", 0, nil, err
	}
	O, _ = encrypt["O"].(string)
	U, _ = encrypt["U"].(string)
	if len(O) != 32 || len(U) != 32 {
		return 0, "", "", 0, nil, fmt.Errorf("malformed PDF: missing O= or U= encryption parameters")
	}
	p, _ := encrypt["P"].(int64)
	return n, O, U, uint32(p), ID, nil
}

// validateKeyLength reads and validates the Length field of the encrypt dict,
// defaulting to 40 when absent.
func validateKeyLength(encrypt dict) (int64, error) {
	n, _ := encrypt["Length"].(int64)
	if n == 0 {
		n = 40
	}
	if n%8 != 0 || n > 128 || n < 40 {
		return 0, fmt.Errorf("malformed PDF: %d-bit encryption key", n)
	}
	return n, nil
}

// extractTrailerID retrieves the first element of the ID array from the trailer.
func extractTrailerID(trailer dict) ([]byte, error) {
	ids, ok := trailer["ID"].(array)
	if !ok || len(ids) < 1 {
		return nil, fmt.Errorf("malformed PDF: missing ID in trailer")
	}
	idstr, ok := ids[0].(string)
	if !ok {
		return nil, fmt.Errorf("malformed PDF: missing ID in trailer")
	}
	return []byte(idstr), nil
}

// parseEncryptHeader validates the V version and R revision of the encrypt dict.
func parseEncryptHeader(encrypt dict) (V, R int64, err error) {
	V, _ = encrypt["V"].(int64)
	if V != 1 && V != 2 && (V != 4 || !okayV4(encrypt)) {
		return 0, 0, fmt.Errorf("unsupported PDF: encryption version V=%d; %v", V, objfmt(encrypt))
	}
	R, _ = encrypt["R"].(int64)
	if R < 2 {
		return 0, 0, fmt.Errorf("malformed PDF: encryption revision R=%d", R)
	}
	if R > 4 {
		return 0, 0, fmt.Errorf("unsupported PDF: encryption revision R=%d", R)
	}
	return V, R, nil
}

// buildEncryptKey computes the RC4 encryption key and cipher from the given
// PDF Standard encryption parameters (PDF 32000-1:2008 §7.6.3.3).
func buildEncryptKey(password, O string, P uint32, n, R int64, ID []byte) ([]byte, *rc4.Cipher, error) {
	pw := []byte(password)
	h := md5.New()
	if len(pw) >= 32 {
		h.Write(pw[:32])
	} else {
		h.Write(pw)
		h.Write(passwordPad[:32-len(pw)])
	}
	h.Write([]byte(O))
	h.Write([]byte{byte(P), byte(P >> 8), byte(P >> 16), byte(P >> 24)})
	h.Write(ID)
	key := h.Sum(nil)
	if R >= 3 {
		for i := 0; i < 50; i++ {
			h.Reset()
			h.Write(key[:n/8])
			key = h.Sum(key[:0])
		}
		key = key[:n/8]
	} else {
		key = key[:40/8]
	}
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed PDF: invalid RC4 key: %v", err)
	}
	return key, c, nil
}

// verifyEncryptKey checks the computed key against the U entry in the
// encryption dictionary (PDF 32000-1:2008 §7.6.3.4).
func verifyEncryptKey(R int64, key []byte, c *rc4.Cipher, U string, ID []byte) bool {
	var u []byte
	if R == 2 {
		u = make([]byte, 32)
		copy(u, passwordPad)
		c.XORKeyStream(u, u)
	} else {
		h := md5.New()
		h.Write(passwordPad)
		h.Write(ID)
		u = h.Sum(nil)
		c.XORKeyStream(u, u)
		for i := 1; i <= 19; i++ {
			key1 := make([]byte, len(key))
			copy(key1, key)
			for j := range key1 {
				key1[j] ^= byte(i)
			}
			c, _ = rc4.NewCipher(key1)
			c.XORKeyStream(u, u)
		}
	}
	return bytes.HasPrefix([]byte(U), u)
}

func (r *Reader) initEncrypt(password string) error {
	encrypt, _ := r.resolve(objptr{}, r.trailer["Encrypt"]).data.(dict)
	n, O, U, P, ID, err := parseEncryptBody(encrypt, r.trailer)
	if err != nil {
		return err
	}
	V, R, err := parseEncryptHeader(encrypt)
	if err != nil {
		return err
	}
	key, c, err := buildEncryptKey(password, O, P, n, R, ID)
	if err != nil {
		return err
	}
	if !verifyEncryptKey(R, key, c, U, ID) {
		return ErrInvalidPassword
	}
	r.key = key
	r.useAES = V == 4
	return nil
}

var ErrInvalidPassword = fmt.Errorf("encrypted PDF: invalid password")

func okayV4(encrypt dict) bool {
	cf, ok := encrypt["CF"].(dict)
	if !ok {
		return false
	}
	stmf, ok := encrypt["StmF"].(name)
	if !ok {
		return false
	}
	strf, ok := encrypt["StrF"].(name)
	if !ok {
		return false
	}
	if stmf != strf {
		return false
	}
	cfparam, _ := cf[stmf].(dict)
	return validateCFParam(cfparam)
}

// validateCFParam checks that the crypt filter parameter dict is compatible
// with the subset of V4 encryption this library supports (AESV2, DocOpen).
func validateCFParam(cfparam dict) bool {
	if cfparam["AuthEvent"] != nil && cfparam["AuthEvent"] != name("DocOpen") {
		return false
	}
	if cfparam["Length"] != nil && cfparam["Length"] != int64(16) {
		return false
	}
	return cfparam["CFM"] == name("AESV2")
}

func cryptKey(key []byte, useAES bool, ptr objptr) []byte {
	h := md5.New()
	h.Write(key)
	h.Write([]byte{byte(ptr.id), byte(ptr.id >> 8), byte(ptr.id >> 16), byte(ptr.gen), byte(ptr.gen >> 8)})
	if useAES {
		h.Write([]byte("sAlT"))
	}
	return h.Sum(nil)
}

func decryptString(key []byte, useAES bool, ptr objptr, x string) string {
	key = cryptKey(key, useAES, ptr)
	if useAES {
		s := []byte(x)
		if len(s) < aes.BlockSize {
			panic("Encrypted text shorter that AES block size")
		}

		block, _ := aes.NewCipher(key)
		iv := s[:aes.BlockSize]
		s = s[aes.BlockSize:]

		stream := cipher.NewCBCDecrypter(block, iv)
		stream.CryptBlocks(s, s)
		x = string(s)
	} else {
		c, _ := rc4.NewCipher(key)
		data := []byte(x)
		c.XORKeyStream(data, data)
		x = string(data)
	}
	return x
}

func decryptStream(key []byte, useAES bool, ptr objptr, rd io.Reader) io.Reader {
	key = cryptKey(key, useAES, ptr)
	if useAES {
		cb, err := aes.NewCipher(key)
		if err != nil {
			panic("AES: " + err.Error())
		}
		iv := make([]byte, 16)
		io.ReadFull(rd, iv)
		cbc := cipher.NewCBCDecrypter(cb, iv)
		rd = &cbcReader{cbc: cbc, rd: rd, buf: make([]byte, 16)}
	} else {
		c, _ := rc4.NewCipher(key)
		rd = &cipher.StreamReader{S: c, R: rd}
	}
	return rd
}

type cbcReader struct {
	cbc  cipher.BlockMode
	rd   io.Reader
	buf  []byte
	pend []byte
}

func (r *cbcReader) Read(b []byte) (n int, err error) {
	if len(r.pend) == 0 {
		_, err = io.ReadFull(r.rd, r.buf)
		if err != nil {
			return 0, err
		}
		r.cbc.CryptBlocks(r.buf, r.buf)
		r.pend = r.buf
	}
	n = copy(b, r.pend)
	r.pend = r.pend[n:]
	return n, nil
}
