package pdf

import (
	"bytes"
	stdlzw "compress/lzw"
	"compress/zlib"
	"encoding/ascii85"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func encodeTestPDFLZW(data []byte, earlyChange int) []byte {
	var out bytes.Buffer
	var bitBuf uint32
	var nBits uint
	width := uint(9)
	emit := func(code uint16) {
		bitBuf |= uint32(code) << (32 - width - nBits)
		nBits += width
		for nBits >= 8 {
			out.WriteByte(byte(bitBuf >> 24))
			bitBuf <<= 8
			nBits -= 8
		}
	}
	hi := uint16(lzwEODCode) // 257; increments before storing, matching compress/lzw writer
	table := make(map[uint32]uint16)
	incHi := func() bool {
		hi++
		if hi == lzwTableSize-1 && earlyChange == 1 {
			emit(lzwClearCode)
			width = 9
			hi = lzwEODCode
			clear(table)
			return true
		}
		if width < lzwMaxWidth && hi == (uint16(1)<<width)-uint16(earlyChange) {
			width++
		}
		if hi == lzwTableSize-1 && earlyChange == 0 {
			emit(lzwClearCode)
			width = 9
			hi = lzwEODCode
			clear(table)
			return true
		}
		return false
	}

	emit(lzwClearCode)
	if len(data) == 0 {
		emit(lzwEODCode)
		if nBits > 0 {
			out.WriteByte(byte(bitBuf >> 24))
		}
		return out.Bytes()
	}

	code := uint16(data[0])
	for _, b := range data[1:] {
		key := (uint32(code) << 8) | uint32(b)
		if next, ok := table[key]; ok {
			code = next
			continue
		}
		emit(code)
		code = uint16(b)
		if !incHi() {
			table[key] = hi
		}
	}
	emit(code)
	_ = incHi()
	emit(lzwEODCode)
	if nBits > 0 {
		out.WriteByte(byte(bitBuf >> 24))
	}
	return out.Bytes()
}

func TestLZWReaderEarlyChangeAndTableTransitions(t *testing.T) {
	// Vary payload past 9->10, 10->11, 11->12 transitions and multiple 4096-entry resets.
	var src bytes.Buffer
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&src, "token-%04d-alpha-beta-gamma ", i)
	}
	want := src.Bytes()

	for _, ec := range []int{1, 0} {
		t.Run(fmt.Sprintf("full table holds until clear earlyChange=%d", ec), func(t *testing.T) {
			var packed bytes.Buffer
			var bits uint32
			var nbits uint
			width := uint(9)
			emit := func(code uint16) {
				bits |= uint32(code) << (32 - width - nbits)
				nbits += width
				for nbits >= 8 {
					packed.WriteByte(byte(bits >> 24))
					bits <<= 8
					nbits -= 8
				}
			}
			emit(256)
			emit(256) // consecutive clears are valid
			for i := 0; i < 5000; i++ {
				emit('A')
				if i > 0 && width < 12 && 258+i == (1<<width)-ec {
					width++
				}
			}
			emit(258) // first dictionary slot must still contain AA
			emit(256)
			width = 9
			emit('B')
			emit(257)
			if nbits > 0 {
				packed.WriteByte(byte(bits >> 24))
			}
			rc, err := newLZWReader(bytes.NewReader(packed.Bytes()), ec)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil || string(got) != strings.Repeat("A", 5002)+"B" {
				t.Fatalf("full table got=%d bytes err=%v", len(got), err)
			}
		})
		t.Run(fmt.Sprintf("earlyChange=%d", ec), func(t *testing.T) {
			encoded := encodeTestPDFLZW(want, ec)
			rc, err := newLZWReader(bytes.NewReader(encoded), ec)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("decoded length %d != want %d", len(got), len(want))
			}
			if err := rc.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := rc.Read(make([]byte, 1)); err == nil {
				t.Fatal("expected error reading after Close")
			}
		})
	}

	t.Run("earlyChange=0 matches stdlib compress/lzw MSB", func(t *testing.T) {
		var stdBuf bytes.Buffer
		w := stdlzw.NewWriter(&stdBuf, stdlzw.MSB, 8)
		if _, err := w.Write(want); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		rc, err := newLZWReader(bytes.NewReader(stdBuf.Bytes()), 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("stdlib MSB decode err=%v, equal=%v", err, bytes.Equal(got, want))
		}
		// Decoding an EarlyChange=1 stream with EarlyChange=0 must fail on width mismatch.
		ec1Stream := encodeTestPDFLZW(want[:4096], 1)
		rcWrong, _ := newLZWReader(bytes.NewReader(ec1Stream), 0)
		if _, err := io.ReadAll(rcWrong); err == nil {
			t.Fatal("expected EarlyChange mismatch to fail decoding")
		}
	})

	t.Run("malformed and truncated streams fail closed", func(t *testing.T) {
		valid := encodeTestPDFLZW([]byte(strings.Repeat("kwkwk pattern ", 80)), 1)
		// Truncate before EOD 257.
		rc, _ := newLZWReader(bytes.NewReader(valid[:len(valid)/2]), 1)
		if _, err := io.ReadAll(rc); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated err = %v, want unexpected EOF", err)
		}
		// Code 300 immediately after Clear 256 (9-bit MSB: 256=0x100, 300=0x12c -> 10000000 01001011 00000000).
		badCode := []byte{0x80, 0x4b, 0x00}
		rcBad, _ := newLZWReader(bytes.NewReader(badCode), 1)
		if _, err := io.ReadAll(rcBad); err == nil {
			t.Fatal("expected invalid code error")
		}
		// Code 258 (nextCode) immediately after Clear 256 with no prevCode (0x100, 0x102 -> 10000000 01000000 10000000).
		badKwKwK := []byte{0x80, 0x40, 0x80}
		rcKw, _ := newLZWReader(bytes.NewReader(badKwKwK), 1)
		if _, err := io.ReadAll(rcKw); err == nil {
			t.Fatal("expected invalid code after clear error")
		}
		if _, err := newLZWReader(bytes.NewReader(valid), 2); err == nil {
			t.Fatal("expected EarlyChange=2 rejection")
		}
	})
}

func TestStreamPredictorsChainsAndBudgets(t *testing.T) {
	t.Run("filter and parameter normalization fails closed", func(t *testing.T) {
		payload := []byte("normalization control")
		for _, tc := range []struct {
			header string
			bad    bool
		}{
			{"/Filter /LZWDecode", false},
			{"/Filter /LZWDecode /DecodeParms [<< /EarlyChange 1 >>]", false},
			{"/Filter [/LZWDecode] /DecodeParms << /EarlyChange 1 >>", false},
			{"/Filter [/LZWDecode] /DecodeParms [null]", false},
			{"/Filter /LZWDecode /DecodeParms 1", true},
			{"/Filter /FlateDecode /DecodeParms 1", true},
			{"/Filter /LZWDecode /DecodeParms []", true},
			{"/Filter /LZWDecode /DecodeParms [null null]", true},
			{"/Filter [/LZWDecode] /DecodeParms [1]", true},
			{"/Filter [/LZWDecode] /DecodeParms [null null]", true},
			{"/Filter [/LZWDecode /LZWDecode] /DecodeParms << >>", true},
			{"/Filter [/LZWDecode /LZWDecode /LZWDecode /LZWDecode /LZWDecode]", true},
			{"/Filter [1]", true},
			{"/Filter /Unknown", true},
			{"/Filter 1", true},
			{"/Filter /LZWDecode /DecodeParms << /EarlyChange -1 >>", true},
			{"/Filter /LZWDecode /DecodeParms << /EarlyChange 2 >>", true},
			{"/Filter /LZWDecode /DecodeParms << /EarlyChange 4294967296 >>", true},
			{"/Filter /LZWDecode /DecodeParms << /EarlyChange 1.5 >>", true},
		} {
			t.Run(tc.header, func(t *testing.T) {
				raw := encodeTestPDFLZW(payload, 1)
				data := singleStreamPDF(fmt.Sprintf("<< %s /Length %d >>", tc.header, len(raw)), raw)
				r, err := NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				rc := r.Page(1).V.Key("Contents").Reader()
				defer rc.Close()
				got, err := io.ReadAll(rc)
				if tc.bad {
					if err == nil {
						t.Fatal("invalid parameters accepted")
					}
				} else if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("got %q, %v", got, err)
				}
			})
		}
	})

	t.Run("predictor sample depths and malformed rows", func(t *testing.T) {
		for _, tc := range []struct {
			bpc       int64
			raw, want []byte
		}{
			{1, []byte{0xc0}, []byte{0x80}},
			{2, []byte{0x50}, []byte{0x60}},
			{4, []byte{0x12}, []byte{0x13}},
			{8, []byte{250, 10}, []byte{250, 4}},
			{16, []byte{0xff, 0xfe, 0, 3}, []byte{0xff, 0xfe, 0, 1}},
		} {
			p, err := applyPredictor(bytes.NewReader(tc.raw), testValue(dict{"Predictor": int64(2), "Columns": int64(2), "BitsPerComponent": tc.bpc}))
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(p)
			if err != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("TIFF bpc=%d got=%v err=%v want=%v", tc.bpc, got, err, tc.want)
			}
		}
		for _, pred := range []int64{2, 10, 11, 12, 13, 14, 15} {
			p, err := applyPredictor(bytes.NewReader([]byte{0, 1}), testValue(dict{"Predictor": pred, "Columns": int64(3)}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.ReadAll(p); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("predictor=%d truncated err=%v", pred, err)
			}
		}
	})

	t.Run("stage read clamping and predictor cumulative charges", func(t *testing.T) {
		for _, cap := range []int64{1, 3, math.MaxInt64} {
			var total atomic.Int64
			r := &boundedStageReader{r: strings.NewReader("abcd"), maxStage: cap, totalUsed: &total, maxTotal: cap}
			n, err := r.Read(make([]byte, 100))
			if cap < 4 {
				if n != 0 || !errors.Is(err, ErrStreamLimit) || total.Load() != cap+1 {
					t.Fatalf("cap=%d n=%d total=%d err=%v", cap, n, total.Load(), err)
				}
			} else if n != 4 || err != nil {
				t.Fatalf("MaxInt64 n=%d err=%v", n, err)
			}
		}
		for _, filter := range []string{"FlateDecode", "LZWDecode"} {
			var enc bytes.Buffer
			raw := []byte{2, 5, 10, 2, 1, 1}
			if filter == "FlateDecode" {
				w := zlib.NewWriter(&enc)
				w.Write(raw)
				w.Close()
			} else {
				enc.Write(encodeTestPDFLZW(raw, 1))
			}
			for _, limit := range []int64{9, 10} {
				var total atomic.Int64
				r, cs, err := applyFilter(bytes.NewReader(enc.Bytes()), filter, testValue(dict{"Predictor": int64(12), "Columns": int64(2)}), 6, &total, limit, nil)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(r)
				(&chainReadCloser{closers: cs}).Close()
				if limit == 9 {
					if !errors.Is(err, ErrStreamLimit) {
						t.Fatalf("%s cap=9 err=%v", filter, err)
					}
				} else if err != nil || !bytes.Equal(got, []byte{5, 10, 6, 11}) || total.Load() != 10 {
					t.Fatalf("%s cap=10 got=%v total=%d err=%v", filter, got, total.Load(), err)
				}
			}
		}
	})

	t.Run("PNG optimum tags 0..4 and TIFF predictor 2", func(t *testing.T) {
		// 5 rows of width 3 (bpp=1) exercising PNG tags 0 (None), 1 (Sub), 2 (Up), 3 (Average), 4 (Paeth).
		rawPNG := []byte{
			0, 10, 20, 30, // row0: [10, 20, 30]
			1, 5, 2, 3, // row1 Sub: [5, 7, 10]
			2, 1, 2, 3, // row2 Up: [6, 9, 13]
			3, 4, 2, 1, // row3 Avg: [4+(0+6)/2=7, 2+(7+9)/2=10, 1+(10+13)/2=12]
			4, 1, 1, 1, // row4 Paeth: [1+paeth(0,7,0)=8, 1+paeth(8,10,6)=11, 1+paeth(11,12,9)=13]
		}
		wantPNG := []byte{
			10, 20, 30,
			5, 7, 10,
			6, 9, 13,
			7, 10, 12,
			8, 11, 13,
		}
		pr, err := applyPredictor(bytes.NewReader(rawPNG), testValue(dict{
			name("Predictor"): int64(15),
			name("Columns"):   int64(3),
		}))
		if err != nil {
			t.Fatal(err)
		}
		gotPNG, err := io.ReadAll(pr)
		if err != nil || !bytes.Equal(gotPNG, wantPNG) {
			t.Fatalf("PNG decoded = (%v, %v), want %v", gotPNG, err, wantPNG)
		}

		// TIFF Predictor 2, 8-bit 2-color 2-column row: [(10,20), (+3,+5)] -> [10, 20, 13, 25].
		tr, err := applyPredictor(bytes.NewReader([]byte{10, 20, 3, 5}), testValue(dict{
			name("Predictor"): int64(2),
			name("Colors"):    int64(2),
			name("Columns"):   int64(2),
		}))
		if err != nil {
			t.Fatal(err)
		}
		gotTIFF, err := io.ReadAll(tr)
		if err != nil || !bytes.Equal(gotTIFF, []byte{10, 20, 13, 25}) {
			t.Fatalf("TIFF decoded = (%v, %v)", gotTIFF, err)
		}

		// Reject oversized/non-positive Columns and unsupported Predictor.
		for _, bad := range []dict{
			{name("Predictor"): int64(12), name("Columns"): int64(0)},
			{name("Predictor"): int64(12), name("Columns"): int64(-4)},
			{name("Predictor"): int64(12), name("Columns"): int64(1 << 25)},
			{name("Predictor"): int64(99), name("Columns"): int64(4)},
		} {
			if _, err := applyPredictor(bytes.NewReader(nil), testValue(bad)); err == nil {
				t.Fatalf("expected error for %v", bad)
			}
		}
	})

	t.Run("ReaderOptions enforces identical budgets across LZW and Flate", func(t *testing.T) {
		payload := []byte("BT /F1 12 Tf 72 720 Td (" + strings.Repeat("A", 2048) + ") Tj ET\n")
		var flateBuf bytes.Buffer
		zw := zlib.NewWriter(&flateBuf)
		_, _ = zw.Write(payload)
		_ = zw.Close()

		var a85LZW bytes.Buffer
		enc85 := ascii85.NewEncoder(&a85LZW)
		_, _ = enc85.Write(encodeTestPDFLZW(payload, 0))
		_ = enc85.Close()
		a85LZW.WriteString("~>")

		for _, tc := range []struct {
			name string
			hdr  string
			body []byte
		}{
			{"Flate", "<< /Filter /FlateDecode /Length %d >>", flateBuf.Bytes()},
			{"LZW default EC1", "<< /Filter /LZWDecode /Length %d >>", encodeTestPDFLZW(payload, 1)},
			{"LZW array dict EC0", "<< /Filter [/LZWDecode] /DecodeParms << /EarlyChange 0 /Predictor 1 >> /Length %d >>", encodeTestPDFLZW(payload, 0)},
			{"ASCII85+LZW empty dict EC0", "<< /Filter [/ASCII85Decode /LZWDecode] /DecodeParms [<< >> << /EarlyChange 0 >>] /Length %d >>", a85LZW.Bytes()},
		} {
			t.Run(tc.name, func(t *testing.T) {
				pdfBytes := singleStreamPDF(fmt.Sprintf(tc.hdr, len(tc.body)), tc.body)
				// Sufficient budget succeeds.
				rOK, err := NewReaderWithOptions(bytes.NewReader(pdfBytes), int64(len(pdfBytes)), ReaderOptions{
					MaxStreamBytes:    4096,
					MaxTotalReadBytes: 8192,
				})
				if err != nil {
					t.Fatal(err)
				}
				txt, err := rOK.Page(1).GetPlainText(nil)
				if err != nil || !strings.Contains(txt, strings.Repeat("A", 2048)) {
					t.Fatalf("ok budget got (%d B, %v)", len(txt), err)
				}
				// Exact per-stream cap boundary: len(payload) succeeds, len(payload)-1 fails with ErrStreamLimit.
				rExact, err := NewReaderWithOptions(bytes.NewReader(pdfBytes), int64(len(pdfBytes)), ReaderOptions{
					MaxStreamBytes:    int64(len(payload)),
					MaxTotalReadBytes: 8192,
				})
				if err != nil {
					t.Fatal(err)
				}
				if tc.name != "ASCII85+LZW empty dict EC0" {
					if txt, err := rExact.Page(1).GetPlainText(nil); err != nil || !strings.Contains(txt, strings.Repeat("A", 2048)) {
						t.Fatalf("exact cap (%d) got (%d B, %v)", len(payload), len(txt), err)
					}
				}
				rMinusOne, err := NewReaderWithOptions(bytes.NewReader(pdfBytes), int64(len(pdfBytes)), ReaderOptions{
					MaxStreamBytes:    int64(len(payload)) - 1,
					MaxTotalReadBytes: 8192,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := rMinusOne.Page(1).GetPlainText(nil); !errors.Is(err, ErrStreamLimit) {
					t.Fatalf("cap-1 expected ErrStreamLimit, got %v", err)
				}
				// Tight per-stream budget fails with ErrStreamLimit and <=1 byte stage overrun.
				rCap, err := NewReaderWithOptions(bytes.NewReader(pdfBytes), int64(len(pdfBytes)), ReaderOptions{
					MaxStreamBytes:    512,
					MaxTotalReadBytes: 8192,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := rCap.Page(1).GetPlainText(nil); !errors.Is(err, ErrStreamLimit) {
					t.Fatalf("expected ErrStreamLimit, got %v", err)
				}
			})
		}
	})

	t.Run("chained intermediate filter and predictor stages count toward MaxTotalReadBytes", func(t *testing.T) {
		smallOp := []byte("BT /F1 12 Tf 72 720 Td (budget control) Tj ET\n")
		// Build a 50,057-byte uncompressed deflate stream (stored 0-length blocks + smallOp)
		// that inflates to 46 bytes, and an analogous LZW Clear-padded stream (~45 KiB -> 46 bytes).
		flateInner := []byte{0x78, 0x01}
		for i := 0; i < 10000; i++ {
			flateInner = append(flateInner, 0, 0, 0, 0xff, 0xff)
		}
		n := uint16(len(smallOp))
		flateInner = append(flateInner, 1, byte(n), byte(n>>8), byte(^n), byte(^n>>8))
		flateInner = append(flateInner, smallOp...)
		// Compute zlib Adler-32 checksum over smallOp.
		s1, s2 := uint32(1), uint32(0)
		for _, b := range smallOp {
			s1 = (s1 + uint32(b)) % 65521
			s2 = (s2 + s1) % 65521
		}
		var adler [4]byte
		binary.BigEndian.PutUint32(adler[:], (s2<<16)|s1)
		flateInner = append(flateInner, adler[:]...)
		var flateOuter bytes.Buffer
		zw := zlib.NewWriter(&flateOuter)
		_, _ = zw.Write(flateInner)
		_ = zw.Close()

		// LZW inner stream padded with 40,000 Clear (256) 9-bit codes (= 45,000 bytes) before smallOp.
		var lzwInnerBits bytes.Buffer
		var bitBuf uint32
		var nBits uint
		emit9 := func(code uint16) {
			bitBuf |= uint32(code) << (32 - 9 - nBits)
			nBits += 9
			for nBits >= 8 {
				lzwInnerBits.WriteByte(byte(bitBuf >> 24))
				bitBuf <<= 8
				nBits -= 8
			}
		}
		for i := 0; i < 40000; i++ {
			emit9(lzwClearCode)
		}
		for _, b := range smallOp {
			emit9(lzwClearCode) // keep width at 9 bits
			emit9(uint16(b))
		}
		emit9(lzwEODCode)
		if nBits > 0 {
			lzwInnerBits.WriteByte(byte(bitBuf >> 24))
		}
		lzwOuter := encodeTestPDFLZW(lzwInnerBits.Bytes(), 1)

		for _, tc := range []struct {
			name   string
			filter string
			raw    []byte
		}{
			{"Flate->Flate", "[/FlateDecode /FlateDecode]", flateOuter.Bytes()},
			{"LZW->LZW", "[/LZWDecode /LZWDecode]", lzwOuter},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var b bytes.Buffer
				b.WriteString("%PDF-1.5\n")
				var offs [10]int
				writeObj := func(id int, body []byte) {
					offs[id-1] = b.Len()
					fmt.Fprintf(&b, "%d 0 obj\n", id)
					b.Write(body)
					b.WriteString("\nendobj\n")
				}
				writeObj(1, []byte("<< /Type /Catalog /Pages 2 0 R >>"))
				writeObj(2, []byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"))
				writeObj(3, []byte("<< /Type /Page /Parent 2 0 R /Contents [4 0 R 5 0 R 6 0 R 7 0 R 8 0 R 9 0 R] /Resources << /Font << /F1 10 0 R >> >> >>"))
				for id := 4; id <= 9; id++ {
					hdr := fmt.Sprintf("<< /Filter %s /Length %d >>\nstream\n", tc.filter, len(tc.raw))
					writeObj(id, append(append([]byte(hdr), tc.raw...), []byte("\nendstream")...))
				}
				writeObj(10, []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"))
				xref := b.Len()
				b.WriteString("xref\n0 11\n0000000000 65535 f \n")
				for _, off := range offs {
					fmt.Fprintf(&b, "%010d 00000 n \n", off)
				}
				fmt.Fprintf(&b, "trailer\n<< /Size 11 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)

				r, err := NewReaderWithOptions(bytes.NewReader(b.Bytes()), int64(b.Len()), ReaderOptions{
					MaxStreamBytes:    65536,
					MaxTotalReadBytes: 262144,
				})
				if err != nil {
					t.Fatal(err)
				}
				_, err = r.Page(1).GetPlainText(nil)
				if !errors.Is(err, ErrStreamLimit) {
					t.Fatalf("expected ErrStreamLimit on cumulative intermediate work, got %v", err)
				}
				if got := r.totalReadBytes.Load(); got <= 262144 || got > 262144+8192 {
					t.Fatalf("totalReadBytes = %d, want just above 262144", got)
				}
			})
		}
	})
}

func TestXRefStreamAndTableResourceBounds(t *testing.T) {
	t.Run("dense capacity and sparse boundary", func(t *testing.T) {
		r := new(Reader)
		for id := uint32(0); id <= maxDenseXrefEntries; id++ {
			entry := xref{ptr: objptr{id, 0}, offset: 1}
			if err := r.setXref(id, entry); err != nil {
				t.Fatal(err)
			}
			if got, ok := r.lookupXref(id); !ok || got != entry {
				t.Fatalf("id=%d got=%v ok=%v", id, got, ok)
			}
			if cap(r.xref) > maxDenseXrefEntries {
				t.Fatalf("dense cap=%d exceeds %d", cap(r.xref), maxDenseXrefEntries)
			}
		}
		if len(r.xref) != maxDenseXrefEntries || len(r.xrefSparse) != 1 {
			t.Fatalf("dense=%d sparse=%d", len(r.xref), len(r.xrefSparse))
		}
	})
	t.Run("sparse high-index XRef stream resolves object without dense slice blowup", func(t *testing.T) {
		var b bytes.Buffer
		b.WriteString("%PDF-1.5\n")
		off1 := b.Len()
		b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
		off2 := b.Len()
		b.WriteString("2 0 obj\n<< /Type /Pages /Kids [1000000 0 R] /Count 1 >>\nendobj\n")
		offSparse := b.Len()
		b.WriteString("1000000 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>\nendobj\n")
		xrefOff := b.Len()
		var raw bytes.Buffer
		putEntry := func(typ byte, f2 uint32) {
			raw.WriteByte(typ)
			var ob [4]byte
			binary.BigEndian.PutUint32(ob[:], f2)
			raw.Write(ob[:])
			raw.Write([]byte{0, 0})
		}
		putEntry(1, uint32(off1))
		putEntry(1, uint32(off2))
		putEntry(1, uint32(offSparse))
		enc := encodeTestPDFLZW(raw.Bytes(), 1)
		fmt.Fprintf(&b, "1000001 0 obj\n<< /Type /XRef /Size 1000002 /Root 1 0 R /W [1 4 2] /Index [1 2 1000000 1] /Filter /LZWDecode /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n",
			len(enc), enc, xrefOff)

		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		r, err := NewReaderWithOptions(bytes.NewReader(b.Bytes()), int64(b.Len()), ReaderOptions{
			MaxStreamBytes:    65536,
			MaxTotalReadBytes: 262144,
		})
		runtime.ReadMemStats(&m1)
		if err != nil {
			t.Fatalf("NewReaderWithOptions: %v", err)
		}
		if len(r.xref) > maxDenseXrefEntries {
			t.Fatalf("dense xref len = %d, want <= %d", len(r.xref), maxDenseXrefEntries)
		}
		if alloc := m1.TotalAlloc - m0.TotalAlloc; alloc > 512*1024 {
			t.Fatalf("allocated %d bytes on sparse XRef, want <= 512 KiB", alloc)
		}
		if r.NumPage() != 1 || r.Page(1).V.IsNull() {
			t.Fatalf("sparse page failed to resolve: NumPage=%d", r.NumPage())
		}
	})

	t.Run("zero-width W, out-of-range Index, cyclic Prev, and entry-amplification XRef fail closed", func(t *testing.T) {
		buildXRefPDF := func(dictExtra string, raw []byte) []byte {
			enc := encodeTestPDFLZW(raw, 1)
			var b bytes.Buffer
			b.WriteString("%PDF-1.5\n")
			xrefOff := b.Len()
			fmt.Fprintf(&b, "1 0 obj\n<< /Type /XRef %s /Filter /LZWDecode /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n",
				dictExtra, len(enc), enc, xrefOff)
			return b.Bytes()
		}
		for _, badDict := range []string{
			"/Size 10 /W [0 0 0] /Index [0 10]",
			"/Size 10 /W [1 0 0] /Index [0 1]",
			"/Size 5 /W [1 1 1] /Index [4 2]",
			"/Size 5 /W [1 1 1] /Index [-1 1]",
			"/Size 5000000000 /W [1 1 1] /Index [0 1]",
		} {
			data := buildXRefPDF(badDict, []byte{1, 0, 0, 1, 0, 0})
			if _, err := NewReaderWithOptions(bytes.NewReader(data), int64(len(data)), ReaderOptions{MaxStreamBytes: 65536, MaxTotalReadBytes: 262144}); err == nil {
				t.Fatalf("expected error for malformed XRef %q", badDict)
			}
		}
		// Cyclic /Prev pointing to its own xref offset (9).
		cyclic := buildXRefPDF("/Size 2 /W [1 1 1] /Index [0 1] /Prev 9", []byte{1, 9, 0})
		if _, err := NewReaderWithOptions(bytes.NewReader(cyclic), int64(len(cyclic)), ReaderOptions{MaxStreamBytes: 65536, MaxTotalReadBytes: 262144}); err == nil {
			t.Fatal("expected error for cyclic XRef Prev")
		}
		// 1-byte record entry amplification (/W [0 1 0], 10,000 entries = 10 KiB < 64 KiB stream cap,
		// but 10,000 * 32 B = 320 KiB > 256 KiB MaxTotalReadBytes).
		amp := buildXRefPDF("/Size 20000 /W [0 1 0] /Index [5000 10000]", make([]byte, 10000))
		if _, err := NewReaderWithOptions(bytes.NewReader(amp), int64(len(amp)), ReaderOptions{MaxStreamBytes: 65536, MaxTotalReadBytes: 262144}); !errors.Is(err, ErrStreamLimit) {
			t.Fatalf("entry amplification err = %v, want ErrStreamLimit", err)
		}
	})

	t.Run("incremental xref free-entry tombstone prevents older Prev resurrection", func(t *testing.T) {
		var b bytes.Buffer
		b.WriteString("%PDF-1.4\n")
		off1 := b.Len()
		b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R /DeletedRef 4 0 R >>\nendobj\n")
		off2 := b.Len()
		b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
		off3 := b.Len()
		b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>\nendobj\n")
		off4 := b.Len()
		b.WriteString("4 0 obj\n<< /Filter /LZWDecode /Length 6 >>\nstream\n\x80\x0b\x60\x50\x22\x0c\nendstream\nendobj\n")
		xref1 := b.Len()
		fmt.Fprintf(&b, "xref\n0 5\n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \n%010d 00000 n \n%010d 00000 n \ntrailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
			off1, off2, off3, off4, xref1)
		xref2 := b.Len()
		fmt.Fprintf(&b, "xref\n4 1\n0000000000 00001 f \ntrailer\n<< /Size 5 /Root 1 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n",
			xref1, xref2)

		r, err := NewReaderWithOptions(bytes.NewReader(b.Bytes()), int64(b.Len()), ReaderOptions{MaxStreamBytes: 65536, MaxTotalReadBytes: 262144})
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Trailer().Key("Root").Key("DeletedRef"); !got.IsNull() {
			t.Fatalf("incrementally freed 4 0 R resolved to %v, want Null", got)
		}
	})
}

func singleStreamPDF(streamDict string, raw []byte) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	var offs [5]int
	writeObj := func(id int, body string) {
		offs[id-1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", id, body)
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	offs[3] = b.Len()
	fmt.Fprintf(&b, "4 0 obj\n%s\nstream\n", streamDict)
	b.Write(raw)
	b.WriteString("\nendstream\nendobj\n")
	writeObj(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	xref := b.Len()
	b.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, off := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}
