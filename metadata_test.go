package pdf

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParsePDFDate(t *testing.T) {
	tests := []struct {
		input    string
		wantZero bool
		wantRFC  string
	}{
		{
			input:   "D:20231015143000+05'30'",
			wantRFC: "2023-10-15T14:30:00+05:30",
		},
		{
			input:   "D:20231015143000Z",
			wantRFC: "2023-10-15T14:30:00Z",
		},
		{
			input:   "D:20231015143000-08'00'",
			wantRFC: "2023-10-15T14:30:00-08:00",
		},
		{
			input:   "D:20231015",
			wantRFC: "2023-10-15T00:00:00Z",
		},
		{
			input:   "D:2023",
			wantRFC: "2023-01-01T00:00:00Z",
		},
		{
			input:    "",
			wantZero: true,
		},
		{
			input:    "garbage",
			wantZero: true,
		},
		{
			input:   "20231015143000Z",
			wantRFC: "2023-10-15T14:30:00Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parsePDFDate(tt.input)
			if tt.wantZero {
				if !got.IsZero() {
					t.Errorf("parsePDFDate(%q) = %v, want zero time", tt.input, got)
				}
				return
			}
			if got.Format(time.RFC3339) != tt.wantRFC {
				t.Errorf("parsePDFDate(%q) = %v, want %v", tt.input, got.Format(time.RFC3339), tt.wantRFC)
			}
		})
	}
}

func buildMinimalPDF(infoContent string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [] /Count 0 >>",
	}
	if infoContent != "" {
		objs = append(objs, "<< "+infoContent+" >>")
	}

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	off := make([]int, len(objs)+1)
	for i, body := range objs {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj %s endobj\n", i+1, body)
	}
	xrefOff := b.Len()
	n := len(objs) + 1
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", n)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	root := "/Root 1 0 R"
	if infoContent != "" {
		root += fmt.Sprintf(" /Info %d 0 R", len(objs))
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d %s >>\nstartxref\n%d\n%%%%EOF\n", n, root, xrefOff)
	return []byte(b.String())
}

func TestInfoFields(t *testing.T) {
	infoContent := "/Title (Test Title) /Author (Test Author) /Subject (Test Subject) /Keywords (foo bar) /Creator (TestApp) /Producer (TestProducer) /CreationDate (D:20231015143000Z)"
	data := buildMinimalPDF(infoContent)

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes failed: %v", err)
	}

	info := r.Info()
	if info.V.IsNull() {
		t.Fatal("Info.V is null, expected a valid Info dict")
	}
	if got := info.Title(); got != "Test Title" {
		t.Errorf("Title() = %q, want %q", got, "Test Title")
	}
	if got := info.Author(); got != "Test Author" {
		t.Errorf("Author() = %q, want %q", got, "Test Author")
	}
	if got := info.Subject(); got != "Test Subject" {
		t.Errorf("Subject() = %q, want %q", got, "Test Subject")
	}
	if got := info.Keywords(); got != "foo bar" {
		t.Errorf("Keywords() = %q, want %q", got, "foo bar")
	}
	if got := info.Creator(); got != "TestApp" {
		t.Errorf("Creator() = %q, want %q", got, "TestApp")
	}
	if got := info.Producer(); got != "TestProducer" {
		t.Errorf("Producer() = %q, want %q", got, "TestProducer")
	}

	wantDate := "2023-10-15T14:30:00Z"
	if got := info.CreationDate().Format(time.RFC3339); got != wantDate {
		t.Errorf("CreationDate() = %v, want %v", got, wantDate)
	}
}

func TestInfoNullWhenNoInfo(t *testing.T) {
	data := buildMinimalPDF("")
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes failed: %v", err)
	}
	if !r.Info().V.IsNull() {
		t.Error("Info().V.IsNull() = false, want true for PDF without /Info")
	}
}
