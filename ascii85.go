// file with help function for ascii85 decoder
// later if new decoders is going to add it reasonable to rename file and add them here
// also create interfaces to switch between them (like in unidoc)

package pdf

import (
	"io"
)

type alphaReader struct {
	reader io.Reader
	eod    bool
}

func newAlphaReader(reader io.Reader) *alphaReader {
	return &alphaReader{reader: reader}
}

func isASCII85(r byte) bool {
	return (r >= '!' && r <= 'u') || r == 'z'
}

func (a *alphaReader) Read(p []byte) (int, error) {
	if a.eod {
		return 0, io.EOF
	}
	n, err := a.reader.Read(p)
	out := 0
	for i := 0; i < n; i++ {
		c := p[i]
		if c == '~' {
			a.eod = true
			return out, io.EOF
		}
		if isASCII85(c) {
			p[out] = c
			out++
		}
	}
	return out, err
}
