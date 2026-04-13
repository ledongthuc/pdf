package pdf

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRecipePDFExtraction(t *testing.T) {
	f, err := os.Open("testdata/ligature_glyphs.pdf")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	r, err := NewReader(f, fi.Size())
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var buf bytes.Buffer
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		text, err := p.GetPlainText(nil)
		if err != nil {
			t.Fatalf("GetPlainText page %d: %v", i, err)
		}
		buf.WriteString(text)
	}
	text := buf.String()

	if len(text) == 0 {
		t.Fatal("extracted text is empty")
	}

	// The key assertion: "Lettuce" must appear correctly decoded
	// (not "Le!uce" from raw byte fallback or PUA chars from unmapped CMap)
	if !strings.Contains(text, "Lettuce") {
		t.Errorf("expected text to contain 'Lettuce', got:\n%s", text[:min(len(text), 200)])
	}
	if strings.Contains(text, "Le!uce") {
		t.Error("text contains garbled 'Le!uce' — ligature decoding failed")
	}

	// Other keywords that should be present
	if !strings.Contains(text, "Tzatziki") {
		t.Error("expected text to contain 'Tzatziki'")
	}
	if !strings.Contains(text, "Worcestershire") {
		t.Error("expected text to contain 'Worcestershire'")
	}
	if !strings.Contains(strings.ToLower(text), "lamb") {
		t.Error("expected text to contain 'lamb'")
	}
	if !strings.Contains(strings.ToLower(text), "chicken") {
		t.Error("expected text to contain 'chicken'")
	}

	// No PUA characters should remain in the output
	for _, r := range text {
		if r >= 0xE000 && r <= 0xE0FF {
			t.Errorf("text contains PUA character U+%04X — unmapped byte leaked through", r)
			break
		}
	}
}

func TestRecipePDFStyledText(t *testing.T) {
	f, err := os.Open("testdata/ligature_glyphs.pdf")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	r, err := NewReader(f, fi.Size())
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	p := r.Page(1)
	content := p.Content()
	if len(content.Text) == 0 {
		t.Fatal("page 1 content has no text entries")
	}

	// Find the "Lettuce" region — look for text entries that form "Lettuce"
	var titleChars strings.Builder
	for _, txt := range content.Text {
		if strings.Contains(txt.Font, "CitrusGothicSolid") {
			titleChars.WriteString(txt.S)
		}
	}
	title := titleChars.String()

	if !strings.Contains(title, "Lettuce") {
		t.Errorf("CitrusGothicSolid text should contain 'Lettuce', got: %q", title)
	}
	if strings.Contains(title, "!") {
		t.Errorf("CitrusGothicSolid text should not contain '!' from garbled ligature, got: %q", title)
	}
}

func TestExamplePDFRegression(t *testing.T) {
	f, err := os.Open("examples/read_plain_text/pdf_test.pdf")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	r, err := NewReader(f, fi.Size())
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var buf bytes.Buffer
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		text, err := p.GetPlainText(nil)
		if err != nil {
			t.Fatalf("GetPlainText page %d: %v", i, err)
		}
		buf.WriteString(text)
	}
	text := buf.String()

	if len(text) == 0 {
		t.Error("example PDF extracted text is empty")
	}
}

func TestExamplePDFStyledTextRegression(t *testing.T) {
	f, err := os.Open("examples/read_text_with_styles/pdf_test.pdf")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	r, err := NewReader(f, fi.Size())
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	texts, err := r.GetStyledTexts()
	if err != nil {
		t.Fatalf("GetStyledTexts: %v", err)
	}

	if len(texts) == 0 {
		t.Error("example PDF styled texts is empty")
	}
}
