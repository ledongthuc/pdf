//go:build ignore

// This program writes testdata/font_encoding.pdf: a one-page PDF that
// reproduces the two font-encoding bugs fixed in ledongthuc/pdf#61.
//
// Usage: go run testdata/font_encoding_gen.go testdata/font_encoding.pdf
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

func main() {
	// F1: a subset TrueType font whose glyph codes were assigned
	// sequentially from 0x21 in order of first use, as subsetters do. Its
	// /Encoding is WinAnsiEncoding, so only /ToUnicode recovers the text;
	// decoding the bytes with WinAnsi yields plausible but wrong letters
	// (e.g. '0' comes out as some other letter).
	line1 := "Case 1:15-cv-07433-LAP"
	codes := map[rune]byte{}
	var order []rune
	var f1 bytes.Buffer
	for _, r := range line1 {
		c, ok := codes[r]
		if !ok {
			c = byte(0x21 + len(order))
			codes[r] = c
			order = append(order, r)
		}
		f1.WriteByte(c)
	}
	var bfchar strings.Builder
	for _, r := range order {
		fmt.Fprintf(&bfchar, "<%02X> <%04X>\n", codes[r], r)
	}
	toUnicode := "/CIDInit /ProcSet findresource begin\n" +
		"12 dict begin\n" +
		"begincmap\n" +
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n" +
		"/CMapName /Adobe-Identity-UCS def\n" +
		"/CMapType 2 def\n" +
		"1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" +
		fmt.Sprintf("%d beginbfchar\n%sendbfchar\n", len(order), bfchar.String()) +
		"endcmap\n" +
		"CMapName currentdict /CMap defineresource pop\n" +
		"end\nend"

	// F2: a nonsymbolic subset Type1 font with an Encoding dictionary of
	// /BaseEncoding /WinAnsiEncoding plus /Differences that move the digits
	// to codes 1-10. Characters outside Differences (the right single quote
	// 0x92 and en dash 0x96) must come from the WinAnsi base, not the raw
	// byte value (which would be C1 control characters).
	line2 := "Defendants’ reply – filed 03/21/16"
	var f2 bytes.Buffer
	for _, r := range line2 {
		switch {
		case r >= '0' && r <= '9':
			f2.WriteByte(byte(r-'0') + 1)
		case r == '’':
			f2.WriteByte(0x92)
		case r == '–':
			f2.WriteByte(0x96)
		default:
			f2.WriteByte(byte(r))
		}
	}

	content := fmt.Sprintf("BT\n/F1 12 Tf\n72 720 Td\n<%X> Tj\nET\nBT\n/F2 12 Tf\n72 700 Td\n<%X> Tj\nET", f1.Bytes(), f2.Bytes())

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	const n = 9
	offsets := make([]int, n)
	obj := func(num int, body string) {
		offsets[num] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	stream := func(num int, data string) {
		obj(num, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(data), data))
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R /F2 7 0 R >> >> /Contents 4 0 R >>")
	stream(4, content)
	obj(5, "<< /Type /Font /Subtype /TrueType /BaseFont /AAAAAA+TimesNewRomanPSMT /Encoding /WinAnsiEncoding /ToUnicode 6 0 R >>")
	stream(6, toUnicode)
	obj(7, "<< /Type /Font /Subtype /Type1 /BaseFont /BBBBBB+Times-Roman /FontDescriptor 8 0 R /Encoding << /Type /Encoding /BaseEncoding /WinAnsiEncoding /Differences [1 /zero /one /two /three /four /five /six /seven /eight /nine] >> >>")
	obj(8, "<< /Type /FontDescriptor /FontName /BBBBBB+Times-Roman /Flags 34 /FontBBox [-168 -218 1000 898] /ItalicAngle 0 /Ascent 683 /Descent -217 /CapHeight 662 /StemV 84 >>")

	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", n)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", n, xref)

	if err := os.WriteFile(os.Args[1], pdf.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
