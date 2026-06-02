// Stream filter decoders: ASCII85, FlateDecode (zlib + PNG Up predictor).

package pdf

import (
	"compress/zlib"
	"encoding/ascii85"
	"fmt"
	"io"
)

const maxDecompressedSize = 256 << 20
const maxPNGColumns = 65536

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

func applyFilter(rd io.Reader, name string, param Value) (io.Reader, error) {
	switch name {
	default:
		return nil, fmt.Errorf("unsupported PDF filter: %s", name)
	case "FlateDecode":
		zr, err := zlib.NewReader(rd)
		if err != nil {
			return nil, fmt.Errorf("FlateDecode: %v", err)
		}
		limited := io.LimitReader(zr, maxDecompressedSize)
		pred := param.Key("Predictor")
		if pred.Kind() == Null {
			return limited, nil
		}
		columns := param.Key("Columns").Int64()
		if columns > maxPNGColumns {
			return nil, fmt.Errorf("FlateDecode: invalid Columns value: %d", columns)
		}
		switch pred.Int64() {
		default:
			if DebugOn {
				fmt.Println("unknown predictor", pred)
			}
			return nil, fmt.Errorf("unsupported FlateDecode predictor: %v", pred.Int64())
		case 12:
			return &pngUpReader{r: limited, hist: make([]byte, 1+columns), tmp: make([]byte, 1+columns)}, nil
		}
	case "ASCII85Decode":
		cleanASCII85 := newAlphaReader(rd)
		decoder := ascii85.NewDecoder(cleanASCII85)

		switch param.Keys() {
		default:
			if DebugOn {
				fmt.Println("param=", param)
			}
			return nil, fmt.Errorf("unexpected DecodeParms for ASCII85Decode")
		case nil:
			return decoder, nil
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
