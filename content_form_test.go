package pdf

import "testing"

// TestContentFormXObject verifies that Content returns the text painted by a
// form XObject, positioned by the CTM at the Do operator and the form's
// /Matrix, and decoded with the fonts of the form's own resources.
func TestContentFormXObject(t *testing.T) {
	form := pdfStream("/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Matrix [1 0 0 1 10 20]"+
		" /Resources << /Font << /F1 5 0 R >> >>",
		"BT /F1 12 Tf (Hi) Tj ET")
	data := buildPagePDF("<< /XObject << /Fm1 6 0 R >> >>",
		"q 1 0 0 1 100 200 cm /Fm1 Do Q",
		helveticaFont, form)

	c := firstPageContent(t, data)
	if got := contentString(c); got != "Hi" {
		t.Fatalf("text = %q, want %q", got, "Hi")
	}
	if x, y := c.Text[0].X, c.Text[0].Y; x != 110 || y != 220 {
		t.Errorf("first glyph at (%v, %v), want (110, 220)", x, y)
	}
	if c.Text[0].Font != "Helvetica" {
		t.Errorf("font = %q, want %q", c.Text[0].Font, "Helvetica")
	}
}

// TestContentFormXObjectPageResources verifies that a form XObject without
// its own /Resources uses the fonts of the content stream that paints it, and
// that the graphics state painting the form is restored after it.
func TestContentFormXObjectPageResources(t *testing.T) {
	form := pdfStream("/Type /XObject /Subtype /Form /BBox [0 0 100 100]",
		"1 0 0 1 300 0 cm BT /F1 10 Tf (B) Tj ET")
	data := buildPagePDF("<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>",
		"/Fm1 Do BT /F1 10 Tf (A) Tj ET",
		helveticaFont, form)

	c := firstPageContent(t, data)
	if got := contentString(c); got != "BA" {
		t.Fatalf("text = %q, want %q", got, "BA")
	}
	if x := c.Text[0].X; x != 300 {
		t.Errorf("form glyph at x = %v, want 300", x)
	}
	if x := c.Text[1].X; x != 0 {
		t.Errorf("page glyph after the form at x = %v, want 0: the form's cm leaked into the page", x)
	}
}

// TestContentFormXObjectCycle verifies that Content terminates on a form
// XObject that paints itself.
func TestContentFormXObjectCycle(t *testing.T) {
	form := pdfStream("/Type /XObject /Subtype /Form /BBox [0 0 100 100]"+
		" /Resources << /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>",
		"BT /F1 10 Tf (x) Tj ET /Fm1 Do")
	data := buildPagePDF("<< /XObject << /Fm1 6 0 R >> >>", "/Fm1 Do", helveticaFont, form)

	c := firstPageContent(t, data)
	if got := contentString(c); len(got) != maxFormDepth {
		t.Errorf("got %d glyphs, want %d (one per nesting level up to maxFormDepth)", len(got), maxFormDepth)
	}
}

// TestContentImageXObject verifies that painting an image XObject adds no
// text.
func TestContentImageXObject(t *testing.T) {
	img := pdfStream("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", "\x00")
	data := buildPagePDF("<< /Font << /F1 5 0 R >> /XObject << /Im1 6 0 R >> >>",
		"/Im1 Do BT /F1 10 Tf (A) Tj ET",
		helveticaFont, img)

	if got := contentString(firstPageContent(t, data)); got != "A" {
		t.Errorf("text = %q, want %q", got, "A")
	}
}
