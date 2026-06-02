package pdf

import (
	"bytes"
	"fmt"
	"io"
)

type xref struct {
	ptr      objptr
	inStream bool
	stream   objptr
	offset   int64
}

func readXref(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	tok := b.readToken()
	if tok == keyword("xref") {
		return readXrefTable(r, b)
	}
	if _, ok := tok.(int64); ok {
		b.unreadToken(tok)
		return readXrefStream(r, b)
	}
	return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", tok)
}

func readXrefStream(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	obj1 := b.readObject()
	obj, ok := obj1.(objdef)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", objfmt(obj1))
	}
	strmptr := obj.ptr
	strm, ok := obj.obj.(stream)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", objfmt(obj))
	}
	if strm.hdr["Type"] != name("XRef") {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref stream does not have type XRef")
	}
	size, ok := strm.hdr["Size"].(int64)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref stream missing Size")
	}
	table := make([]xref, size)
	table, err := readXrefStreamData(r, strm, table, size)
	if err != nil {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %v", err)
	}
	if table, err = followXrefStreamPrevChain(r, table, size, strm.hdr); err != nil {
		return nil, objptr{}, nil, err
	}
	return table, strmptr, strm.hdr, nil
}

func followXrefStreamPrevChain(r *Reader, table []xref, size int64, hdr dict) ([]xref, error) {
	for prevoff := hdr["Prev"]; prevoff != nil; {
		off, ok := prevoff.(int64)
		if !ok {
			return nil, fmt.Errorf("malformed PDF: xref Prev is not integer: %v", prevoff)
		}
		nextTable, nextPrev, err := applyPrevXrefStream(r, off, table, size)
		if err != nil {
			return nil, fmt.Errorf("malformed PDF: %v", err)
		}
		table, prevoff = nextTable, nextPrev
	}
	return table, nil
}

// applyPrevXrefStream loads one "Prev" xref stream at absOffset, applies it to
// table, and returns the updated table and the next Prev value (nil when the
// chain ends).
func applyPrevXrefStream(r *Reader, absOffset int64, table []xref, maxSize int64) ([]xref, interface{}, error) {
	b := newBuffer(io.NewSectionReader(r.f, absOffset, r.end-absOffset), absOffset)
	obj1 := b.readObject()
	obj, ok := obj1.(objdef)
	if !ok {
		return nil, nil, fmt.Errorf("xref prev stream not found: %v", objfmt(obj1))
	}
	prevstrm, ok := obj.obj.(stream)
	if !ok {
		return nil, nil, fmt.Errorf("xref prev stream not found: %v", objfmt(obj))
	}
	prev := Value{r, objptr{}, prevstrm}
	if prev.Kind() != Stream {
		return nil, nil, fmt.Errorf("xref prev stream is not stream: %v", prev)
	}
	if prev.Key("Type").Name() != "XRef" {
		return nil, nil, fmt.Errorf("xref prev stream does not have type XRef")
	}
	psize := prev.Key("Size").Int64()
	if psize > maxSize {
		return nil, nil, fmt.Errorf("xref prev stream larger than last stream")
	}
	var err error
	if table, err = readXrefStreamData(r, prev.data.(stream), table, psize); err != nil {
		return nil, nil, fmt.Errorf("reading xref prev stream: %v", err)
	}
	return table, prevstrm.hdr["Prev"], nil
}

// parseWArray validates and converts the PDF xref-stream W array to []int.
func parseWArray(ww array) ([]int, error) {
	var w []int
	for _, x := range ww {
		i, ok := x.(int64)
		if !ok || int64(int(i)) != i {
			return nil, fmt.Errorf("invalid W array %v", objfmt(ww))
		}
		w = append(w, int(i))
	}
	if len(w) < 3 {
		return nil, fmt.Errorf("invalid W array %v", objfmt(ww))
	}
	return w, nil
}

// processXrefEntry reads one entry from an xref stream and updates table.
func processXrefEntry(buf []byte, w []int, start int64, i int, table []xref, data io.ReadCloser) ([]xref, error) {
	if _, err := io.ReadFull(data, buf); err != nil {
		return nil, fmt.Errorf("error reading xref stream: %v", err)
	}
	v1 := decodeInt(buf[0:w[0]])
	if w[0] == 0 {
		v1 = 1
	}
	v2 := decodeInt(buf[w[0] : w[0]+w[1]])
	v3 := decodeInt(buf[w[0]+w[1] : w[0]+w[1]+w[2]])
	x := int(start) + i
	var err error
	table, err = ensureXrefSlot(table, x)
	if err != nil {
		return nil, err
	}
	if table[x].ptr != (objptr{}) {
		return table, nil
	}
	switch v1 {
	case 0:
		table[x] = xref{ptr: objptr{0, 65535}}
	case 1:
		table[x] = xref{ptr: objptr{uint32(x), uint16(v3)}, offset: int64(v2)}
	case 2:
		table[x] = xref{ptr: objptr{uint32(x), 0}, inStream: true, stream: objptr{uint32(v2), 0}, offset: int64(v3)}
	default:
		if DebugOn {
			fmt.Printf("invalid xref stream type %d: %x\n", v1, buf)
		}
	}
	return table, nil
}

func readXrefStreamData(r *Reader, strm stream, table []xref, size int64) ([]xref, error) {
	index, _ := strm.hdr["Index"].(array)
	if index == nil {
		index = array{int64(0), size}
	}
	if len(index)%2 != 0 {
		return nil, fmt.Errorf("invalid Index array %v", objfmt(index))
	}
	ww, ok := strm.hdr["W"].(array)
	if !ok {
		return nil, fmt.Errorf("xref stream missing W array")
	}
	w, err := parseWArray(ww)
	if err != nil {
		return nil, err
	}
	v := Value{r, objptr{}, strm}
	wtotal := 0
	for _, wid := range w {
		wtotal += wid
	}
	buf := make([]byte, wtotal)
	data := v.Reader()
	return processXrefIndexPairs(data, buf, w, index, table)
}

func processXrefIndexPairs(data io.ReadCloser, buf []byte, w []int, index array, table []xref) ([]xref, error) {
	for len(index) > 0 {
		start, ok1 := index[0].(int64)
		n, ok2 := index[1].(int64)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("malformed Index pair %v %v %T %T", objfmt(index[0]), objfmt(index[1]), index[0], index[1])
		}
		index = index[2:]
		for i := 0; i < int(n); i++ {
			var err error
			table, err = processXrefEntry(buf, w, start, i, table, data)
			if err != nil {
				return nil, err
			}
		}
	}
	return table, nil
}

func decodeInt(b []byte) int {
	x := 0
	for _, c := range b {
		x = x<<8 | int(c)
	}
	return x
}

func followXrefTablePrevChain(r *Reader, table []xref, trailer dict) ([]xref, error) {
	for prevoff := trailer["Prev"]; prevoff != nil; {
		off, ok := prevoff.(int64)
		if !ok {
			return nil, fmt.Errorf("malformed PDF: xref Prev is not integer: %v", prevoff)
		}
		nextTable, nextPrev, err := applyPrevXrefTable(r, off, table)
		if err != nil {
			return nil, fmt.Errorf("malformed PDF: %v", err)
		}
		table, prevoff = nextTable, nextPrev
	}
	return table, nil
}

// applyPrevXrefTable loads one "Prev" xref table at absOffset, applies it to
// table, and returns the updated table and the next Prev value (nil when the
// chain ends).
func applyPrevXrefTable(r *Reader, absOffset int64, table []xref) ([]xref, interface{}, error) {
	b := newBuffer(io.NewSectionReader(r.f, absOffset, r.end-absOffset), absOffset)
	if tok := b.readToken(); tok != keyword("xref") {
		return nil, nil, fmt.Errorf("xref Prev does not point to xref")
	}
	var err error
	if table, err = readXrefTableData(b, table); err != nil {
		return nil, nil, err
	}
	trailer, ok := b.readObject().(dict)
	if !ok {
		return nil, nil, fmt.Errorf("xref Prev table not followed by trailer dictionary")
	}
	return table, trailer["Prev"], nil
}

const maxXrefObjects = 8_388_607 // PDF spec max indirect object number

func ensureXrefSlot(table []xref, x int) ([]xref, error) {
	if x < 0 || x > maxXrefObjects {
		return nil, fmt.Errorf("malformed PDF: xref object number out of range: %d", x)
	}
	for cap(table) <= x {
		table = append(table[:cap(table)], xref{})
	}
	if len(table) <= x {
		table = table[:x+1]
	}
	return table, nil
}

// readXrefTableEntry reads and validates one xref table entry (offset gen alloc).
func readXrefTableEntry(b *buffer) (off, gen int64, alloc keyword, err error) {
	var ok1, ok2, ok3 bool
	off, ok1 = b.readToken().(int64)
	gen, ok2 = b.readToken().(int64)
	alloc, ok3 = b.readToken().(keyword)
	if !ok1 || !ok2 || !ok3 || alloc != keyword("f") && alloc != keyword("n") {
		return 0, 0, "", fmt.Errorf("malformed xref table")
	}
	return
}

func readXrefTable(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	var table []xref
	table, err := readXrefTableData(b, table)
	if err != nil {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %v", err)
	}
	trailer, ok := b.readObject().(dict)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref table not followed by trailer dictionary")
	}
	if table, err = followXrefTablePrevChain(r, table, trailer); err != nil {
		return nil, objptr{}, nil, err
	}
	size, ok := trailer[name("Size")].(int64)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: trailer missing /Size entry")
	}
	if size < int64(len(table)) {
		table = table[:size]
	}
	return table, objptr{}, trailer, nil
}

func applyXrefTableSection(b *buffer, start int64, n int64, table []xref) ([]xref, error) {
	for i := 0; i < int(n); i++ {
		off, gen, alloc, err := readXrefTableEntry(b)
		if err != nil {
			return nil, err
		}
		x := int(start) + i
		table, err = ensureXrefSlot(table, x)
		if err != nil {
			return nil, err
		}
		if alloc == "n" && table[x].offset == 0 {
			table[x] = xref{ptr: objptr{uint32(x), uint16(gen)}, offset: int64(off)}
		}
	}
	return table, nil
}

func readXrefTableData(b *buffer, table []xref) ([]xref, error) {
	for {
		tok := b.readToken()
		if tok == keyword("trailer") {
			break
		}
		start, ok1 := tok.(int64)
		n, ok2 := b.readToken().(int64)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("malformed xref table")
		}
		var err error
		table, err = applyXrefTableSection(b, start, n, table)
		if err != nil {
			return nil, err
		}
	}
	return table, nil
}

func findLastLine(buf []byte, s string) int {
	bs := []byte(s)
	max := len(buf)
	for {
		i := bytes.LastIndex(buf[:max], bs)
		if i <= 0 || i+len(bs) >= len(buf) {
			return -1
		}
		if (buf[i-1] == '\n' || buf[i-1] == '\r') && (buf[i+len(bs)] == '\n' || buf[i+len(bs)] == '\r') {
			return i
		}
		max = i
	}
}

// findStartxrefFallback scans f backwards for %%EOF when it is not located
// within the last endChunk bytes, then returns the absolute file offset of
// the preceding "startxref" line.  Called only for non-conformant PDFs that
// append data (e.g. a digital signature) after %%EOF.
func findStartxrefFallback(f io.ReaderAt, size int64) (int64, error) {
	const scanChunk = 4096
	const overlap = 4 // len("%%EOF")-1: handles %%EOF straddling a chunk boundary

	var suffix []byte
	for pos := size; pos > 0; {
		start := pos - scanChunk
		if start < 0 {
			start = 0
		}
		chunkLen := pos - start
		combined := make([]byte, chunkLen+int64(len(suffix)))
		f.ReadAt(combined[:chunkLen], start)
		copy(combined[chunkLen:], suffix)

		if idx := bytes.LastIndex(combined, []byte("%%EOF")); idx >= 0 {
			eofAbs := start + int64(idx)
			ctxStart := eofAbs - 512
			if ctxStart < 0 {
				ctxStart = 0
			}
			ctx := make([]byte, eofAbs-ctxStart)
			f.ReadAt(ctx, ctxStart)
			j := findLastLine(ctx, "startxref")
			if j < 0 {
				return -1, fmt.Errorf("%%EOF found but startxref missing")
			}
			return ctxStart + int64(j), nil
		}

		if chunkLen >= int64(overlap) {
			suffix = make([]byte, overlap)
			copy(suffix, combined[:overlap])
		} else {
			suffix = make([]byte, chunkLen)
			copy(suffix, combined[:chunkLen])
		}
		pos = start
	}
	return -1, fmt.Errorf("%%EOF not found in file")
}

func isValidPDFTerminator(b byte) bool {
	return b == '\r' || b == '\n' || b == ' ' || b == '\t'
}

// validatePDFHeader reads the first 10 bytes of f and returns an error when
// the %PDF-n.m header is missing or malformed.
func validatePDFHeader(f io.ReaderAt) error {
	buf := make([]byte, 10)
	f.ReadAt(buf, 0)
	if !bytes.HasPrefix(buf, []byte("%PDF-1.")) || buf[7] < '0' || buf[7] > '7' || !isValidPDFTerminator(buf[8]) {
		return fmt.Errorf("not a PDF file: invalid header")
	}
	return nil
}

// findStartxrefOffset locates the "startxref" keyword near the end of f and
// returns its absolute byte offset.  It searches the last 1024 bytes first;
// when %%EOF is not found there, it falls back to a full reverse scan.
func findStartxrefOffset(f io.ReaderAt, size int64) (int64, error) {
	const endChunk = 1024
	readStart := size - endChunk
	if readStart < 0 {
		readStart = 0
	}
	buf := make([]byte, size-readStart)
	f.ReadAt(buf, readStart)
	buf = bytes.TrimRight(buf, "\r\n\t ")
	if bytes.HasSuffix(buf, []byte("%%EOF")) {
		i := findLastLine(buf, "startxref")
		if i < 0 {
			return -1, fmt.Errorf("malformed PDF file: missing final startxref")
		}
		return readStart + int64(i), nil
	}
	pos, err := findStartxrefFallback(f, size)
	if err != nil {
		return -1, fmt.Errorf("not a PDF file: missing %%%%EOF")
	}
	return pos, nil
}
