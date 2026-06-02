// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// A TextEncoding represents a mapping between
// font code points and UTF-8 text.
type TextEncoding interface {
	// Decode returns the UTF-8 text corresponding to
	// the sequence of code points in raw.
	Decode(raw string) (text string)
}

type nopEncoder struct {
}

func (e *nopEncoder) Decode(raw string) (text string) {
	return raw
}

// multibyteCMapEncoder decodes PDF content-stream bytes using an x/text Encoding.
// Used for predefined CMaps whose raw bytes are a well-known legacy encoding
// (e.g. Shift-JIS for 90ms-RKSJ-H). Silently falls back to raw bytes on error.
type multibyteCMapEncoder struct {
	enc encoding.Encoding
}

func (e *multibyteCMapEncoder) Decode(raw string) (text string) {
	decoded, err := e.enc.NewDecoder().Bytes([]byte(raw))
	if err != nil {
		return raw
	}
	return string(decoded)
}

// ucs2BEEncoder decodes PDF content-stream bytes encoded as UCS-2 big-endian.
// Used for predefined CMaps such as UniGB-UCS2-H/V, UniCNS-UCS2-H/V,
// UniJIS-UCS2-H/V, and UniKS-UCS2-H/V. Each glyph selector is a 2-byte
// big-endian Unicode code point (e.g. 中 = 0x4E2D). No external dependency needed.
// UCS-2/BMP-only — no surrogate-pair handling; correct for Uni*-UCS2-* CMaps.
type ucs2BEEncoder struct{}

func (e *ucs2BEEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		r = append(r, rune(uint16(raw[i])<<8|uint16(raw[i+1])))
	}
	return string(r)
}

type byteEncoder struct {
	table *[256]rune
}

func (e *byteEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		r = append(r, e.table[raw[i]])
	}
	return string(r)
}

// encoderForCMapName returns the TextEncoding for a named PDF CMap/Encoding.
func encoderForCMapName(n string) TextEncoding {
	switch n {
	case "WinAnsiEncoding":
		return &byteEncoder{&winAnsiEncoding}
	case "MacRomanEncoding":
		return &byteEncoder{&macRomanEncoding}
	case "Identity-H":
		return &byteEncoder{&pdfDocEncoding}
	case "90ms-RKSJ-H", "90ms-RKSJ-V", "90pv-RKSJ-H":
		return &multibyteCMapEncoder{japanese.ShiftJIS}
	case "UniGB-UCS2-H", "UniGB-UCS2-V",
		"UniCNS-UCS2-H", "UniCNS-UCS2-V",
		"UniJIS-UCS2-H", "UniJIS-UCS2-V",
		"UniKS-UCS2-H", "UniKS-UCS2-V":
		return &ucs2BEEncoder{}
	case "GB-EUC-H", "GB-EUC-V",
		"GBKp-EUC-H", "GBKp-EUC-V",
		"GBK-EUC-H", "GBK-EUC-V":
		return &multibyteCMapEncoder{simplifiedchinese.GBK}
	case "ETen-B5-H", "ETen-B5-V",
		"ETenms-B5-H", "ETenms-B5-V":
		return &multibyteCMapEncoder{traditionalchinese.Big5}
	case "KSCms-UHC-H", "KSCms-UHC-V",
		"KSC-EUC-H", "KSC-EUC-V",
		"KSCms-UHC-HW-H", "KSCms-UHC-HW-V":
		return &multibyteCMapEncoder{korean.EUCKR}
	default:
		if DebugOn {
			println("unknown encoding", n)
		}
		return &byteEncoder{&pdfDocEncoding}
	}
}
