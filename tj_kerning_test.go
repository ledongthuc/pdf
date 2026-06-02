// tj_kerning_test.go — tests for TJ operator kerning (numeric array elements).
//
// Reproduces the class of issue reported in unidoc/unipdf #524, where TJ arrays
// containing large negative kerning values cause word-spacing to be silently lost.
//
// Fixed in interpretTJArray (content.go): numeric TJ elements whose magnitude
// exceeds tjSpaceThreshold (120 thousandths of a text-space unit) now emit a
// synthetic space before the next string segment, matching the MuPDF/Poppler
// convention.
package pdf

import (
	"context"
	"strings"
	"testing"
)

// buildTJKerningPDF wraps a TJ content stream in a minimal one-page PDF.
// It reuses buildSinglePagePDF from page_test.go (same package).
//
// stream must be a valid PDF content stream body.
func buildTJKerningPDF(stream string) []byte {
	return buildSinglePagePDF(stream)
}

// joinContentText concatenates the S fields of all Text entries from
// Content().Text into one string, making whitespace gaps visible.
func joinContentText(texts []Text) string {
	var b strings.Builder
	for _, t := range texts {
		b.WriteString(t.S)
	}
	return b.String()
}

// TestTJKerningLargeGap exercises a TJ array with a large negative kern (-300).
//
// PDF spec §9.4.5: a negative number displaces the current text position to the
// right by (number / 1000) * Tfs units.  -300 at 12pt = 3.6 pt rightward shift,
// which is larger than a typical space glyph (~3 pt) and must be treated as a
// word gap by any conforming text extractor.
func TestTJKerningLargeGap(t *testing.T) {
	// TJ array: (Hello) -300 (World)
	// No font resource — nopEncoder passes ASCII through unchanged.
	stream := "BT\n/F1 12 Tf\n[(Hello) -300 (World)] TJ\nET"
	data := buildTJKerningPDF(stream)

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)

	// ---- Content().Text check ----
	texts := p.Content().Text
	joined := joinContentText(texts)
	t.Logf("Content().Text joined: %q", joined)

	if !strings.Contains(joined, "Hello") {
		t.Errorf("Content().Text: \"Hello\" not found in %q (characters dropped)", joined)
	}
	if !strings.Contains(joined, "World") {
		t.Errorf("Content().Text: \"World\" not found in %q (characters dropped)", joined)
	}

	// The discriminating check: a -300 kern is a word gap; the output must
	// contain whitespace between the two words.
	between := strings.TrimPrefix(joined, "Hello")
	between = strings.TrimSuffix(strings.TrimSuffix(between, "\n"), "World")
	hasGap := strings.ContainsAny(between, " \t\n") || !strings.Contains(joined, "HelloWorld")
	if !hasGap {
		t.Errorf("Content().Text: large kern (-300) did not produce whitespace between words; got %q (want gap between \"Hello\" and \"World\")", joined)
	}

	// ---- GetStyledTexts check ----
	ctx := context.Background()
	sentences, err := r.GetStyledTexts(ctx)
	if err != nil {
		t.Fatalf("GetStyledTexts: %v", err)
	}
	t.Logf("GetStyledTexts sentences (%d):", len(sentences))
	for i, s := range sentences {
		t.Logf("  [%d] Font=%q FontSize=%.2f X=%.2f Y=%.2f S=%q", i, s.Font, s.FontSize, s.X, s.Y, s.S)
	}

	allText := func() string {
		var b strings.Builder
		for _, s := range sentences {
			b.WriteString(s.S)
		}
		return b.String()
	}()

	if !strings.Contains(allText, "Hello") {
		t.Errorf("GetStyledTexts: \"Hello\" not found in %q", allText)
	}
	if !strings.Contains(allText, "World") {
		t.Errorf("GetStyledTexts: \"World\" not found in %q", allText)
	}
}

// TestTJKerningSmallKern exercises a TJ array with a small kern (-10).
//
// -10 at 12pt = 0.12 pt, a purely typographic adjustment that must NOT
// introduce a word-space.  The merged output should be "HelloWorld" (no gap).
// This case is expected to PASS — the library's concatenation behaviour is
// correct for small kerning values.
func TestTJKerningSmallKern(t *testing.T) {
	stream := "BT\n/F1 12 Tf\n[(Hello) -10 (World)] TJ\nET"
	data := buildTJKerningPDF(stream)

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)

	texts := p.Content().Text
	joined := joinContentText(texts)
	t.Logf("Content().Text joined (small kern): %q", joined)

	if !strings.Contains(joined, "Hello") {
		t.Errorf("Content().Text: \"Hello\" not found in %q", joined)
	}
	if !strings.Contains(joined, "World") {
		t.Errorf("Content().Text: \"World\" not found in %q", joined)
	}

	// Small kern must NOT introduce a gap.
	if !strings.Contains(joined, "HelloWorld") {
		t.Errorf("Content().Text: small kern (-10) should NOT introduce whitespace; got %q", joined)
	}
}

// TestTJKerningPositiveKern exercises a TJ array with a positive kern (+200).
//
// A positive number moves the text position LEFT (closer together / overlapping).
// This is a ligature-like adjustment and must also not drop characters.
func TestTJKerningPositiveKern(t *testing.T) {
	stream := "BT\n/F1 12 Tf\n[(Hello) 200 (World)] TJ\nET"
	data := buildTJKerningPDF(stream)

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)

	texts := p.Content().Text
	joined := joinContentText(texts)
	t.Logf("Content().Text joined (positive kern): %q", joined)

	if !strings.Contains(joined, "Hello") {
		t.Errorf("Content().Text: \"Hello\" not found in %q (positive kern dropped it)", joined)
	}
	if !strings.Contains(joined, "World") {
		t.Errorf("Content().Text: \"World\" not found in %q (positive kern dropped it)", joined)
	}
}

// TestTJKerningMultipleSegments exercises a TJ array with multiple alternating
// strings and kerning values: (A) -300 (B) -10 (C) -300 (D).
//
// Large kerns (-300) before B and D produce spaces; the small kern (-10)
// before C does not.  Expected joined output: "A BC D\n".
func TestTJKerningMultipleSegments(t *testing.T) {
	stream := "BT\n/F1 12 Tf\n[(A) -300 (B) -10 (C) -300 (D)] TJ\nET"
	data := buildTJKerningPDF(stream)

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)

	texts := p.Content().Text
	joined := joinContentText(texts)
	t.Logf("Content().Text joined (multi-segment): %q", joined)

	for _, ch := range []string{"A", "B", "C", "D"} {
		if !strings.Contains(joined, ch) {
			t.Errorf("Content().Text: %q not found in %q (character dropped)", ch, joined)
		}
	}

	// Large kerns (-300) before B and before D must produce a space gap.
	if !strings.Contains(joined, "A B") {
		t.Errorf("Content().Text: -300 kern between A and B did not produce a space; got %q", joined)
	}
	// Small kern (-10) between B and C must NOT produce a space.
	if !strings.Contains(joined, "BC") {
		t.Errorf("Content().Text: -10 kern between B and C should not produce a space; got %q", joined)
	}
	// Large kern (-300) before D must produce a space gap.
	if !strings.Contains(joined, "C D") {
		t.Errorf("Content().Text: -300 kern between C and D did not produce a space; got %q", joined)
	}
}
