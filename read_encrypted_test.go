package pdf

import (
	"os"
	"strings"
	"testing"
)

// A one-line page encrypted by pdfcpu with 40-bit RC4 (/V 1 /R 2) and an empty
// user password. PDF 32000-1 Algorithm 1 truncates the per-object key to
// min(n/8+5, 16) bytes: 10 here, where the whole digest is only right for a
// 128-bit key.
func TestReads40BitRC4WithEmptyUserPassword(t *testing.T) {
	for _, bits := range []string{"40"} {
		t.Run(bits+"-bit", func(t *testing.T) {
			f, err := os.Open("testdata/rc4-" + bits + ".pdf")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			st, _ := f.Stat()

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("reader panicked: %v", r)
				}
			}()
			r, err := NewReaderEncrypted(f, st.Size(), func() string { return "" })
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			text, err := r.Page(1).GetPlainText(nil)
			if err != nil {
				t.Fatalf("page text: %v", err)
			}
			if !strings.Contains(text, "forty bit rc4 reads this line") {
				t.Fatalf("page text %q does not carry the line", text)
			}
		})
	}
}
