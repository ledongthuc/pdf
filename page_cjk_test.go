// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"testing"

	"golang.org/x/text/encoding/japanese"
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
