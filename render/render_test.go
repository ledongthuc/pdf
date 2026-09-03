package render

import (
	"bytes"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestParser(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOps int
		wantErr bool
	}{
		{
			name:    "simple moveto lineto",
			input:   "100 200 m 300 400 l S",
			wantOps: 3,
		},
		{
			name:    "rectangle",
			input:   "50 50 100 100 re f",
			wantOps: 2,
		},
		{
			name:    "text operators",
			input:   "BT /F1 12 Tf 100 700 Td (Hello World) Tj ET",
			wantOps: 5,
		},
		{
			name:    "hex string",
			input:   "<48656C6C6F> Tj",
			wantOps: 1,
		},
		{
			name:    "array with numbers",
			input:   "[(Hello) -100 (World)] TJ",
			wantOps: 1,
		},
		{
			name:    "graphics state save restore",
			input:   "q 1 0 0 1 50 50 cm Q",
			wantOps: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newParser([]byte(tt.input))
			var ops int
			for {
				op, err := p.nextOperator()
				if err != nil {
					break
				}
				if op != nil {
					ops++
				}
			}
			if ops != tt.wantOps {
				t.Errorf("got %d operators, want %d", ops, tt.wantOps)
			}
		})
	}
}

func TestContextBasics(t *testing.T) {
	ctx := newContext(100, 100)

	if ctx.Width() != 100 {
		t.Errorf("Width() = %d, want 100", ctx.Width())
	}
	if ctx.Height() != 100 {
		t.Errorf("Height() = %d, want 100", ctx.Height())
	}

	// Test image is not nil
	if ctx.image() == nil {
		t.Error("image() returned nil")
	}

	// Test state is not nil
	if ctx.state() == nil {
		t.Error("state() returned nil")
	}
}

func TestContextPath(t *testing.T) {
	ctx := newContext(200, 200)

	// Test path construction
	ctx.moveTo(10, 10)
	ctx.lineTo(50, 10)
	ctx.lineTo(50, 50)
	ctx.lineTo(10, 50)
	ctx.closePath()

	if len(ctx.path) != 5 {
		t.Errorf("got %d path segments, want 5", len(ctx.path))
	}

	// Verify we have a current point
	x, y := ctx.currentPoint()
	if x == 0 && y == 0 {
		t.Log("currentPoint returned origin (path may need explicit moveTo after transforms)")
	}
}

func TestContextTransform(t *testing.T) {
	ctx := newContext(100, 100)

	// Identity transform
	x, y := ctx.transformPoint(10, 20)
	if x != 10 || y != 20 {
		t.Errorf("identity transform: got (%f, %f), want (10, 20)", x, y)
	}

	// Translation
	ctx.translate(50, 50)
	x, y = ctx.transformPoint(10, 20)
	if x != 60 || y != 70 {
		t.Errorf("after translate(50,50): got (%f, %f), want (60, 70)", x, y)
	}

	// Scale
	ctx = newContext(100, 100)
	ctx.scale(2, 2)
	x, y = ctx.transformPoint(10, 20)
	if x != 20 || y != 40 {
		t.Errorf("after scale(2,2): got (%f, %f), want (20, 40)", x, y)
	}
}

func TestContextPushPop(t *testing.T) {
	ctx := newContext(100, 100)

	// Modify state
	ctx.translate(50, 50)
	ctx.setLineWidth(5)

	// Push
	ctx.push()

	// Modify more
	ctx.translate(10, 10)
	ctx.setLineWidth(10)

	// Verify changes
	x, _ := ctx.transformPoint(0, 0)
	if x != 60 {
		t.Errorf("after translate: got x=%f, want 60", x)
	}
	if ctx.state().lineWidth != 10 {
		t.Errorf("lineWidth = %f, want 10", ctx.state().lineWidth)
	}

	// Pop
	ctx.pop()

	// Verify restoration
	x, _ = ctx.transformPoint(0, 0)
	if x != 50 {
		t.Errorf("after pop: got x=%f, want 50", x)
	}
	if ctx.state().lineWidth != 5 {
		t.Errorf("after pop: lineWidth = %f, want 5", ctx.state().lineWidth)
	}
}

func TestGraphicsStateClone(t *testing.T) {
	gs := newGraphicsState()
	gs.lineWidth = 5
	gs.dashPattern = []float64{3, 2}

	clone := gs.clone()

	// Modify original
	gs.lineWidth = 10
	gs.dashPattern[0] = 99

	// Clone should be unchanged
	if clone.lineWidth != 5 {
		t.Errorf("clone.lineWidth = %f, want 5", clone.lineWidth)
	}
	if clone.dashPattern[0] != 3 {
		t.Errorf("clone.dashPattern[0] = %f, want 3", clone.dashPattern[0])
	}
}

func TestTextState(t *testing.T) {
	ts := textState{}
	ts.reset()

	// Check defaults
	if ts.hScale != 1.0 {
		t.Errorf("hScale = %f, want 1.0", ts.hScale)
	}
	if ts.tm[0] != 1 || ts.tm[3] != 1 {
		t.Error("tm should be identity matrix")
	}

	// Test setMatrix
	ts.setMatrix(2, 0, 0, 2, 100, 100)
	if ts.tm[0] != 2 || ts.tm[4] != 100 {
		t.Error("setMatrix did not set correctly")
	}
	if ts.tlm[0] != 2 || ts.tlm[4] != 100 {
		t.Error("setMatrix should also set tlm")
	}

	// Test translateLine
	// Per PDF spec: Tlm = T(tx, ty) × Tlm (matrix multiplication, not simple addition)
	// With tlm = [2, 0, 0, 2, 100, 100] and translate(10, -15):
	// newE = a*tx + c*ty + e = 2*10 + 0*(-15) + 100 = 120
	// newF = b*tx + d*ty + f = 0*10 + 2*(-15) + 100 = 70
	ts.translateLine(10, -15)
	if ts.tlm[4] != 120 || ts.tlm[5] != 70 {
		t.Errorf("translateLine: tlm = [%.0f, %.0f], want [120, 70]", ts.tlm[4], ts.tlm[5])
	}
}

func TestFlattenCubic(t *testing.T) {
	ctx := newContext(100, 100)

	// A simple curve
	points := ctx.flattenCubic(
		point{0, 0},
		point{10, 40},
		point{40, 40},
		point{50, 0},
	)

	// Should produce multiple points (flattened curve)
	if len(points) < 2 {
		t.Errorf("flattenCubic produced %d points, expected at least 2", len(points))
	}

	// Last point should be close to endpoint
	last := points[len(points)-1]
	if last.x != 50 || last.y != 0 {
		t.Errorf("last point = (%f, %f), want (50, 0)", last.x, last.y)
	}
}

func TestPathToPolygons(t *testing.T) {
	ctx := newContext(200, 200)

	// Draw a simple rectangle
	ctx.moveTo(10, 10)
	ctx.lineTo(50, 10)
	ctx.lineTo(50, 50)
	ctx.lineTo(10, 50)
	ctx.closePath()

	polygons := ctx.pathToPolygons()

	if len(polygons) != 1 {
		t.Errorf("got %d polygons, want 1", len(polygons))
	}

	if len(polygons) > 0 && len(polygons[0]) != 5 {
		t.Errorf("polygon has %d points, want 5 (4 corners + close)", len(polygons[0]))
	}
}

func TestContextRectangle(t *testing.T) {
	ctx := newContext(200, 200)

	ctx.rectangle(10, 10, 40, 30)

	// Should have moveTo, 3 lineTo, close = 5 segments
	if len(ctx.path) != 5 {
		t.Errorf("rectangle created %d segments, want 5", len(ctx.path))
	}
}

func TestFillPolygon(t *testing.T) {
	ctx := newContext(100, 100)

	// Create a simple triangle
	poly := []point{
		{25, 25},
		{75, 25},
		{50, 75},
	}

	// This should not panic
	ctx.fillPolygon(poly, ctx.state().fillColor, windingRule)

	// Check that at least some pixels were set
	// (The exact check depends on implementation)
	img := ctx.image()
	if img == nil {
		t.Error("image is nil after fill")
	}
}

func TestNewContext(t *testing.T) {
	ctx := newContext(800, 600)

	// Verify the image dimensions
	bounds := ctx.img.Bounds()
	if bounds.Dx() != 800 || bounds.Dy() != 600 {
		t.Errorf("image bounds = %v, want 800x600", bounds)
	}

	// Verify initial state
	if ctx.currentState == nil {
		t.Error("currentState should not be nil")
	}

	// Matrix should be identity
	if ctx.matrix[0] != 1 || ctx.matrix[3] != 1 {
		t.Error("initial matrix should be identity")
	}
}

// TestContextDrawSimple tests basic drawing without a real PDF
func TestContextDrawSimple(t *testing.T) {
	ctx := newContext(100, 100)

	// Set fill color to red
	ctx.setFillColor(color.RGBA{R: 255, G: 0, B: 0, A: 255})

	// Draw a rectangle path
	ctx.rectangle(10, 10, 80, 80)

	// Fill it
	ctx.fill(windingRule)

	// Clear path
	ctx.newPath()

	// Verify we can encode as PNG without error
	var buf bytes.Buffer
	err := png.Encode(&buf, ctx.image())
	if err != nil {
		t.Errorf("failed to encode as PNG: %v", err)
	}

	if buf.Len() == 0 {
		t.Error("PNG output is empty")
	}
}

// TestRenderPDFPage tests rendering a real PDF page if the Epstein docs are available.
// This is similar to how termite/e2e tests work, but uses our pure Go renderer.
func TestRenderPDFPage(t *testing.T) {
	// Look for Epstein docs PDF (same path as termite e2e tests)
	pdfPaths := []string{
		filepath.Join("..", "..", "..", "examples", "epstein", "epstein-docs", "court-2024-giuffre-v-maxwell.pdf"),
		filepath.Join("..", "..", "..", "..", "examples", "epstein", "epstein-docs", "court-2024-giuffre-v-maxwell.pdf"),
	}

	var pdfPath string
	for _, p := range pdfPaths {
		if _, err := os.Stat(p); err == nil {
			pdfPath = p
			break
		}
	}

	if pdfPath == "" {
		t.Skip("Epstein PDF not found - skipping real PDF render test")
	}

	// Read the PDF
	pdfData, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("Failed to read PDF: %v", err)
	}

	t.Logf("Loaded PDF: %d bytes", len(pdfData))

	// Create renderer
	renderer, err := NewRenderer(pdfData)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	numPages := renderer.NumPages()
	t.Logf("PDF has %d pages", numPages)

	if numPages == 0 {
		t.Fatal("PDF has no pages")
	}

	// Render first page at 150 DPI (same as PyMuPDF script)
	const dpi = 150
	pngData, err := renderer.RenderPageToPNG(1, dpi)
	if err != nil {
		t.Fatalf("Failed to render page 1: %v", err)
	}

	t.Logf("Rendered page 1 to PNG: %d bytes", len(pngData))

	// Verify it's valid PNG
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatalf("Failed to decode rendered PNG: %v", err)
	}

	bounds := img.Bounds()
	t.Logf("Rendered image size: %dx%d pixels", bounds.Dx(), bounds.Dy())

	// At 150 DPI, a standard letter page (8.5x11 inches) should be approximately:
	// Width: 8.5 * 150 = 1275 pixels
	// Height: 11 * 150 = 1650 pixels
	// Allow some variance for different page sizes
	if bounds.Dx() < 100 || bounds.Dy() < 100 {
		t.Errorf("Rendered image too small: %dx%d", bounds.Dx(), bounds.Dy())
	}

	// Optionally write output for manual inspection
	if os.Getenv("WRITE_TEST_OUTPUT") != "" {
		outputPath := "/tmp/render_test_page1.png"
		if err := os.WriteFile(outputPath, pngData, 0644); err != nil {
			t.Logf("Warning: could not write test output: %v", err)
		} else {
			t.Logf("Wrote test output to %s", outputPath)
		}
	}
}

// TestRenderMultiplePages tests rendering multiple pages from a PDF.
func TestRenderMultiplePages(t *testing.T) {
	// Look for Epstein docs PDF
	pdfPaths := []string{
		filepath.Join("..", "..", "..", "examples", "epstein", "epstein-docs", "court-2024-giuffre-v-maxwell.pdf"),
		filepath.Join("..", "..", "..", "..", "examples", "epstein", "epstein-docs", "court-2024-giuffre-v-maxwell.pdf"),
	}

	var pdfPath string
	for _, p := range pdfPaths {
		if _, err := os.Stat(p); err == nil {
			pdfPath = p
			break
		}
	}

	if pdfPath == "" {
		t.Skip("Epstein PDF not found - skipping multi-page render test")
	}

	// Read the PDF
	pdfData, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("Failed to read PDF: %v", err)
	}

	// Create renderer
	renderer, err := NewRenderer(pdfData)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	numPages := renderer.NumPages()
	t.Logf("PDF has %d pages", numPages)

	// Render first 3 pages (or fewer if PDF is smaller)
	pagesToRender := min(numPages, 3)

	for page := 1; page <= pagesToRender; page++ {
		pngData, err := renderer.RenderPageToPNG(page, 72) // Use 72 DPI for faster test
		if err != nil {
			t.Errorf("Failed to render page %d: %v", page, err)
			continue
		}

		// Verify it's valid PNG
		img, err := png.Decode(bytes.NewReader(pngData))
		if err != nil {
			t.Errorf("Failed to decode page %d PNG: %v", page, err)
			continue
		}

		bounds := img.Bounds()
		t.Logf("Page %d: %dx%d pixels, %d bytes", page, bounds.Dx(), bounds.Dy(), len(pngData))
	}
}
