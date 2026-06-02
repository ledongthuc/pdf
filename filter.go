// Stream filter decoders: ASCII85, FlateDecode (zlib + PNG Up predictor).

package pdf

import (
	"compress/zlib"
	"encoding/ascii85"
	"fmt"
	"io"
)

type alphaReader struct {
	reader io.Reader
}

func newAlphaReader(reader io.Reader) *alphaReader {
	return &alphaReader{reader: reader}
}

func checkASCII85(r byte) byte {
	if r >= '!' && r <= 'u' { // 33 <= ascii85 <=117
		return r
	}
	if r == '~' {
		return 1 // for marking possible end of data
	}
	return 0 // if non-ascii85
}

func (a *alphaReader) Read(p []byte) (int, error) {
	n, err := a.reader.Read(p)
	if err == io.EOF {
	}
	if err != nil {
		return n, err
	}
	buf := make([]byte, n)
	tilda := false
	for i := 0; i < n; i++ {
		char := checkASCII85(p[i])
		if char == '>' && tilda { // end of data
			break
		}
		if char > 1 {
			buf[i] = char
		}
		if char == 1 {
			tilda = true // possible end of data
		}
	}

	copy(p, buf)
	return n, nil
}

func applyFilter(rd io.Reader, name string, param Value) io.Reader {
	switch name {
	default:
		panic("unknown filter " + name)
	case "FlateDecode":
		zr, err := zlib.NewReader(rd)
		if err != nil {
			panic(err)
		}
		pred := param.Key("Predictor")
		if pred.Kind() == Null {
			return zr
		}
		columns := param.Key("Columns").Int64()
		switch pred.Int64() {
		default:
			if DebugOn {
				fmt.Println("unknown predictor", pred)
			}
			panic("pred")
		case 12:
			return &pngUpReader{r: zr, hist: make([]byte, 1+columns), tmp: make([]byte, 1+columns)}
		}
	case "ASCII85Decode":
		cleanASCII85 := newAlphaReader(rd)
		decoder := ascii85.NewDecoder(cleanASCII85)

		switch param.Keys() {
		default:
			if DebugOn {
				fmt.Println("param=", param)
			}
			panic("not expected DecodeParms for ascii85")
		case nil:
			return decoder
		}
	}
}

type pngUpReader struct {
	r    io.Reader
	hist []byte
	tmp  []byte
	pend []byte
}

func (r *pngUpReader) Read(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		if len(r.pend) > 0 {
			m := copy(b, r.pend)
			n += m
			b = b[m:]
			r.pend = r.pend[m:]
			continue
		}
		_, err := io.ReadFull(r.r, r.tmp)
		if err != nil {
			return n, err
		}
		if r.tmp[0] != 2 {
			return n, fmt.Errorf("malformed PNG-Up encoding")
		}
		for i, b := range r.tmp {
			r.hist[i] += b
		}
		r.pend = r.hist[1:]
	}
	return n, nil
}
