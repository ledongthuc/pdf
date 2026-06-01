// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"testing"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// TestUCS2BEEncoder verifies that ucs2BEEncoder correctly decodes UCS-2
// big-endian byte sequences produced by Uni*-UCS2-H/V predefined CMaps.
func TestUCS2BEEncoder(t *testing.T) {
	tests := []struct {
		name string
		// raw holds two-byte UCS-2 BE pairs for each rune.
		raw  []byte
		want string
	}{
		{
			name: "simplified Chinese characters",
			// 中(0x4E2D) 文(0x6587) 测(0x6D4B) 试(0x8BD5)
			raw:  []byte{0x4E, 0x2D, 0x65, 0x87, 0x6D, 0x4B, 0x8B, 0xD5},
			want: "中文测试",
		},
		{
			name: "traditional Chinese characters",
			// 繁(0x7E41) 體(0x9AD4) 中(0x4E2D) 文(0x6587)
			raw:  []byte{0x7E, 0x41, 0x9A, 0xD4, 0x4E, 0x2D, 0x65, 0x87},
			want: "繁體中文",
		},
		{
			name: "Japanese hiragana",
			// あ(0x3042) い(0x3044) う(0x3046)
			raw:  []byte{0x30, 0x42, 0x30, 0x44, 0x30, 0x46},
			want: "あいう",
		},
		{
			name: "Korean hangul",
			// 한(0xD55C) 국(0xAD6D) 어(0xC5B4)
			raw:  []byte{0xD5, 0x5C, 0xAD, 0x6D, 0xC5, 0xB4},
			want: "한국어",
		},
		{
			name: "ASCII via UCS-2",
			// A(0x0041) B(0x0042)
			raw:  []byte{0x00, 0x41, 0x00, 0x42},
			want: "AB",
		},
		{
			name: "empty input",
			raw:  []byte{},
			want: "",
		},
		{
			name: "trailing odd byte is ignored",
			// 中(0x4E2D) + one trailing byte
			raw:  []byte{0x4E, 0x2D, 0xFF},
			want: "中",
		},
	}

	enc := &ucs2BEEncoder{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enc.Decode(string(tt.raw))
			if got != tt.want {
				t.Errorf("Decode: got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMultibyteCMapEncoder_ShiftJIS verifies that multibyteCMapEncoder
// correctly decodes Shift-JIS bytes produced by 90ms-RKSJ-H/V predefined CMaps.
func TestMultibyteCMapEncoder_ShiftJIS(t *testing.T) {
	enc := &multibyteCMapEncoder{japanese.ShiftJIS}

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "Japanese katakana word",
			// テスト (te-su-to) in Shift-JIS
			raw:  []byte{0x83, 0x65, 0x83, 0x58, 0x83, 0x67},
			want: "テスト",
		},
		{
			name: "mixed kanji and hiragana",
			// 日本語 in Shift-JIS
			raw:  []byte{0x93, 0xFA, 0x96, 0x7B, 0x8C, 0xEA},
			want: "日本語",
		},
		{
			name: "empty input",
			raw:  []byte{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enc.Decode(string(tt.raw))
			if got != tt.want {
				t.Errorf("Decode: got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMultibyteCMapEncoder_GBK verifies that multibyteCMapEncoder correctly
// decodes GBK bytes produced by GBK-EUC-H/V predefined CMaps.
func TestMultibyteCMapEncoder_GBK(t *testing.T) {
	enc := &multibyteCMapEncoder{simplifiedchinese.GBK}

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "simplified Chinese characters",
			// 中(0xD6D0) 国(0xB9FA) 你(0xC4E3) 好(0xBAC3) in GBK
			raw:  []byte{0xD6, 0xD0, 0xB9, 0xFA, 0xC4, 0xE3, 0xBA, 0xC3},
			want: "中国你好",
		},
		{
			name: "empty input",
			raw:  []byte{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enc.Decode(string(tt.raw))
			if got != tt.want {
				t.Errorf("Decode: got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMultibyteCMapEncoder_Big5 verifies that multibyteCMapEncoder correctly
// decodes Big5-ETen bytes produced by ETen-B5-H/V predefined CMaps.
func TestMultibyteCMapEncoder_Big5(t *testing.T) {
	enc := &multibyteCMapEncoder{traditionalchinese.Big5}

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "traditional Chinese characters",
			// 癒(0xC2A1) 體(0xC5E9) 中(0xA4A4) 文(0xA4E5) in Big5-ETen
			raw:  []byte{0xC2, 0xA1, 0xC5, 0xE9, 0xA4, 0xA4, 0xA4, 0xE5},
			want: "癒體中文",
		},
		{
			name: "empty input",
			raw:  []byte{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enc.Decode(string(tt.raw))
			if got != tt.want {
				t.Errorf("Decode: got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMultibyteCMapEncoder_UHC verifies that multibyteCMapEncoder correctly
// decodes EUC-KR/UHC bytes produced by KSCms-UHC-H/V predefined CMaps.
func TestMultibyteCMapEncoder_UHC(t *testing.T) {
	enc := &multibyteCMapEncoder{korean.EUCKR}

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "Korean characters",
			// 안(0xBEC8) 녕(0xB3E7) 하(0xC7CF) 펲(0xBC84) 퓭(0xBF94) in EUC-KR
			raw:  []byte{0xBE, 0xC8, 0xB3, 0xE7, 0xC7, 0xCF, 0xBC, 0x84, 0xBF, 0x94},
			want: "안녕하펲퓭",
		},
		{
			name: "empty input",
			raw:  []byte{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enc.Decode(string(tt.raw))
			if got != tt.want {
				t.Errorf("Decode: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMultibyteCMapEncoder_GBEUC(t *testing.T) {
	// Bytes sourced from Python: '中文'.encode('gb2312') = [0xd6,0xd0,0xce,0xc4]
	// GB2312 is a strict subset of GBK — same byte layout for these chars
	enc := &multibyteCMapEncoder{simplifiedchinese.GBK}
	tests := []struct{ name string; raw []byte; want string }{
		{
			name: "GB-EUC: 中文 via GB2312 bytes",
			raw:  []byte{0xd6, 0xd0, 0xce, 0xc4},
			want: "中文",
		},
		{
			name: "GBKp-EUC: same bytes are GBKp-compatible",
			raw:  []byte{0xd6, 0xd0, 0xce, 0xc4},
			want: "中文",
		},
		{name: "empty", raw: []byte{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enc.Decode(string(tt.raw))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMultibyteCMapEncoder_ETenmsB5(t *testing.T) {
	// Bytes sourced from Python: '中文'.encode('big5') = [0xa4,0xa4,0xa4,0xe5]
	// ETenms differs from ETen only in punctuation range 0xA1A1-0xA1FE;
	// main character blocks are identical
	enc := &multibyteCMapEncoder{traditionalchinese.Big5}
	tests := []struct{ name string; raw []byte; want string }{
		{
			name: "ETenms-B5: 中文 via Big5 bytes",
			raw:  []byte{0xa4, 0xa4, 0xa4, 0xe5},
			want: "中文",
		},
		{name: "empty", raw: []byte{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enc.Decode(string(tt.raw))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMultibyteCMapEncoder_KSCEUC(t *testing.T) {
	// Bytes sourced from Python: '한국어'.encode('euc-kr') = [0xc7,0xd1,0xb1,0xb9,0xbe,0xee]
	// KSC 5601 is EUC-KR predecessor; UHC-HW shares same byte encoding
	enc := &multibyteCMapEncoder{korean.EUCKR}
	tests := []struct{ name string; raw []byte; want string }{
		{
			name: "KSC-EUC: 한국어 via EUC-KR bytes",
			raw:  []byte{0xc7, 0xd1, 0xb1, 0xb9, 0xbe, 0xee},
			want: "한국어",
		},
		{
			name: "KSCms-UHC-HW: same encoding",
			raw:  []byte{0xc7, 0xd1, 0xb1, 0xb9, 0xbe, 0xee},
			want: "한국어",
		},
		{name: "empty", raw: []byte{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enc.Decode(string(tt.raw))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
