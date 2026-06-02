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

// cmapEncoderTable maps each predefined PDF CMap/Encoding name to its TextEncoding.
// Encoders are stateless singletons; keys that share a charset share the same pointer.
var cmapEncoderTable = func() map[string]TextEncoding {
	shiftJIS := &multibyteCMapEncoder{japanese.ShiftJIS}
	gbk := &multibyteCMapEncoder{simplifiedchinese.GBK}
	big5 := &multibyteCMapEncoder{traditionalchinese.Big5}
	euckr := &multibyteCMapEncoder{korean.EUCKR}
	ucs2 := &ucs2BEEncoder{}
	return map[string]TextEncoding{
		"WinAnsiEncoding":  &byteEncoder{&winAnsiEncoding},
		"MacRomanEncoding": &byteEncoder{&macRomanEncoding},
		"Identity-H":       &byteEncoder{&pdfDocEncoding},
		"90ms-RKSJ-H":      shiftJIS,
		"90ms-RKSJ-V":      shiftJIS,
		"90pv-RKSJ-H":      shiftJIS,
		"UniGB-UCS2-H":     ucs2,
		"UniGB-UCS2-V":     ucs2,
		"UniCNS-UCS2-H":    ucs2,
		"UniCNS-UCS2-V":    ucs2,
		"UniJIS-UCS2-H":    ucs2,
		"UniJIS-UCS2-V":    ucs2,
		"UniKS-UCS2-H":     ucs2,
		"UniKS-UCS2-V":     ucs2,
		"GB-EUC-H":         gbk,
		"GB-EUC-V":         gbk,
		"GBKp-EUC-H":       gbk,
		"GBKp-EUC-V":       gbk,
		"GBK-EUC-H":        gbk,
		"GBK-EUC-V":        gbk,
		"ETen-B5-H":        big5,
		"ETen-B5-V":        big5,
		"ETenms-B5-H":      big5,
		"ETenms-B5-V":      big5,
		"KSCms-UHC-H":      euckr,
		"KSCms-UHC-V":      euckr,
		"KSC-EUC-H":        euckr,
		"KSC-EUC-V":        euckr,
		"KSCms-UHC-HW-H":   euckr,
		"KSCms-UHC-HW-V":   euckr,
	}
}()

// encoderForCMapName returns the TextEncoding for a named PDF CMap/Encoding.
func encoderForCMapName(n string) TextEncoding {
	if enc, ok := cmapEncoderTable[n]; ok {
		return enc
	}
	if DebugOn {
		println("unknown encoding", n)
	}
	return &byteEncoder{&pdfDocEncoding}
}
