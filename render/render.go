// Package render provides pure Go PDF page rendering to images.
//
// This package renders PDF pages to raster images without requiring external
// dependencies like CGO, Ghostscript, or system libraries. It implements a
// subset of PDF operators sufficient for rendering most document pages.
//
// Supported features:
//   - Path operations (moveto, lineto, curveto, closepath, rectangle)
//   - Fill and stroke operations (winding and even-odd rules)
//   - Graphics state (transformations, line width, line cap/join, dash patterns)
//   - Color spaces (DeviceRGB, DeviceCMYK, DeviceGray)
//   - Text rendering (Tf, Tj, TJ, Tm with basic font support)
//   - Image XObjects (inline and external images)
//   - Clipping paths
//
// Example usage:
//
//	renderer, err := render.NewRenderer(pdfBytes)
//	if err != nil {
//	    return err
//	}
//	defer renderer.Close()
//
//	img, err := renderer.RenderPage(1, 150) // page 1 at 150 DPI
//	if err != nil {
//	    return err
//	}
package render

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/ajroetker/go-jpeg2000"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// DefaultDPI is the default resolution for rendering.
const DefaultDPI = 150.0

// Renderer renders PDF pages to images.
type Renderer struct {
	ctx       *model.Context
	fontCache *fontCache
}

// NewRenderer creates a new renderer from PDF bytes.
func NewRenderer(pdfData []byte) (*Renderer, error) {
	rs := bytes.NewReader(pdfData)
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed

	ctx, err := pdfcpu.Read(rs, conf)
	if err != nil {
		return nil, fmt.Errorf("read pdf: %w", err)
	}

	// Ensure page count is calculated
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("ensure page count: %w", err)
	}

	return &Renderer{
		ctx:       ctx,
		fontCache: newFontCache(ctx),
	}, nil
}

// NewRendererFromReader creates a new renderer from an io.ReadSeeker.
func NewRendererFromReader(rs io.ReadSeeker) (*Renderer, error) {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed

	ctx, err := pdfcpu.Read(rs, conf)
	if err != nil {
		return nil, fmt.Errorf("read pdf: %w", err)
	}

	// Ensure page count is calculated
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("ensure page count: %w", err)
	}

	return &Renderer{
		ctx:       ctx,
		fontCache: newFontCache(ctx),
	}, nil
}

// Close releases resources associated with the renderer.
func (r *Renderer) Close() error {
	return nil
}

// NumPages returns the number of pages in the PDF.
func (r *Renderer) NumPages() int {
	return r.ctx.PageCount
}

// RenderPage renders a page to an image at the specified DPI.
// pageNum is 1-indexed.
func (r *Renderer) RenderPage(pageNum int, dpi float64) (image.Image, error) {
	if dpi <= 0 {
		dpi = DefaultDPI
	}

	if pageNum < 1 || pageNum > r.ctx.PageCount {
		return nil, fmt.Errorf("page %d out of range [1, %d]", pageNum, r.ctx.PageCount)
	}

	// Get page dictionary
	pageDict, _, inhPAttrs, err := r.ctx.PageDict(pageNum, false)
	if err != nil {
		return nil, fmt.Errorf("get page dict: %w", err)
	}

	// Get media box (page dimensions)
	mediaBox := inhPAttrs.MediaBox
	if mediaBox == nil {
		return nil, errors.New("page has no media box")
	}

	// Calculate image dimensions
	scale := dpi / 72.0
	width := int(math.Ceil(mediaBox.Width() * scale))
	height := int(math.Ceil(mediaBox.Height() * scale))

	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid page dimensions: %dx%d", width, height)
	}

	// Create rendering context
	ctx := newContext(width, height)

	// Set up coordinate system:
	// PDF origin is bottom-left, image origin is top-left
	ctx.translate(0, float64(height))
	ctx.scale(scale, -scale)

	// Apply media box offset
	ctx.translate(-mediaBox.LL.X, -mediaBox.LL.Y)

	// Get content streams
	contents, err := r.getPageContents(pageDict)
	if err != nil {
		return nil, fmt.Errorf("get contents: %w", err)
	}

	// Get resources
	resources, err := r.getPageResources(pageDict, inhPAttrs)
	if err != nil {
		return nil, fmt.Errorf("get resources: %w", err)
	}

	// Render content stream
	if err := r.renderContentStream(ctx, contents, resources); err != nil {
		return nil, fmt.Errorf("render content: %w", err)
	}

	return ctx.image(), nil
}

// RenderPageToPNG renders a page to PNG bytes.
func (r *Renderer) RenderPageToPNG(pageNum int, dpi float64) ([]byte, error) {
	img, err := r.RenderPage(pageNum, dpi)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}

	return buf.Bytes(), nil
}

// getPageContents extracts the content stream(s) from a page.
func (r *Renderer) getPageContents(pageDict types.Dict) ([]byte, error) {
	obj, found := pageDict.Find("Contents")
	if !found {
		return nil, nil // Empty page
	}

	obj, err := r.ctx.Dereference(obj)
	if err != nil {
		return nil, err
	}

	switch v := obj.(type) {
	case types.StreamDict:
		return r.decodeStream(&v)
	case types.Array:
		var combined bytes.Buffer
		for _, elem := range v {
			elem, err := r.ctx.Dereference(elem)
			if err != nil {
				return nil, err
			}
			if sd, ok := elem.(types.StreamDict); ok {
				data, err := r.decodeStream(&sd)
				if err != nil {
					return nil, err
				}
				combined.Write(data)
				combined.WriteByte('\n')
			}
		}
		return combined.Bytes(), nil
	default:
		return nil, fmt.Errorf("unexpected contents type: %T", obj)
	}
}

// decodeStream decodes a stream's content.
func (r *Renderer) decodeStream(sd *types.StreamDict) ([]byte, error) {
	if err := sd.Decode(); err != nil {
		return nil, fmt.Errorf("decode stream: %w", err)
	}
	return sd.Content, nil
}

// getPageResources gets the resources dictionary for a page.
func (r *Renderer) getPageResources(pageDict types.Dict, inhPAttrs *model.InheritedPageAttrs) (*resources, error) {
	res := &resources{
		ctx:    r.ctx,
		fonts:  make(map[string]*fontInfo),
		images: make(map[string]*imageInfo),
	}

	// Try page resources first, fall back to inherited
	var resDict types.Dict
	if obj, found := pageDict.Find("Resources"); found {
		obj, err := r.ctx.Dereference(obj)
		if err != nil {
			return nil, err
		}
		if d, ok := obj.(types.Dict); ok {
			resDict = d
		}
	}
	if resDict == nil && inhPAttrs.Resources != nil {
		resDict = inhPAttrs.Resources
	}

	if resDict == nil {
		return res, nil
	}

	res.dict = resDict

	// Parse fonts
	if fontDict, err := r.getDictEntry(resDict, "Font"); err == nil && fontDict != nil {
		for name := range fontDict {
			res.fonts[name] = nil // Lazy load
		}
	}

	// Parse XObjects
	if xobjDict, err := r.getDictEntry(resDict, "XObject"); err == nil && xobjDict != nil {
		for name := range xobjDict {
			res.images[name] = nil // Lazy load
		}
	}

	return res, nil
}

// getDictEntry gets a dictionary entry from a dictionary.
func (r *Renderer) getDictEntry(d types.Dict, key string) (types.Dict, error) {
	obj, found := d.Find(key)
	if !found {
		return nil, nil
	}

	obj, err := r.ctx.Dereference(obj)
	if err != nil {
		return nil, err
	}

	if dict, ok := obj.(types.Dict); ok {
		return dict, nil
	}
	return nil, nil
}

// renderContentStream renders a PDF content stream.
func (r *Renderer) renderContentStream(ctx *context, content []byte, res *resources) error {
	parser := newParser(content)
	state := ctx.state()

	for {
		op, err := parser.nextOperator()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Log but continue on parse errors
			continue
		}

		if err := r.executeOperator(ctx, state, op, res); err != nil {
			// Log but continue on execution errors
			continue
		}
	}

	return nil
}

// executeOperator executes a single PDF operator.
func (r *Renderer) executeOperator(ctx *context, state *graphicsState, op *operator, res *resources) error {
	switch op.name {
	// Graphics state operators
	case "q":
		ctx.push()
	case "Q":
		ctx.pop()
	case "cm":
		if len(op.operands) != 6 {
			return errors.New("cm requires 6 operands")
		}
		a := toFloat(op.operands[0])
		b := toFloat(op.operands[1])
		c := toFloat(op.operands[2])
		d := toFloat(op.operands[3])
		e := toFloat(op.operands[4])
		f := toFloat(op.operands[5])
		ctx.transform(a, b, c, d, e, f)
	case "w":
		if len(op.operands) >= 1 {
			ctx.setLineWidth(toFloat(op.operands[0]))
		}
	case "J":
		if len(op.operands) >= 1 {
			ctx.setLineCap(int(toFloat(op.operands[0])))
		}
	case "j":
		if len(op.operands) >= 1 {
			ctx.setLineJoin(int(toFloat(op.operands[0])))
		}
	case "M":
		if len(op.operands) >= 1 {
			ctx.setMiterLimit(toFloat(op.operands[0]))
		}
	case "d":
		// Dash pattern [array] phase
		if len(op.operands) >= 2 {
			// Parse dash array
			if arr, ok := op.operands[0].([]any); ok {
				pattern := make([]float64, len(arr))
				for i, v := range arr {
					pattern[i] = toFloat(v)
				}
				phase := toFloat(op.operands[1])
				ctx.setDash(pattern, phase)
			}
		}
	case "gs":
		// Graphics state from ExtGState - parse if available
		// For now, just acknowledge the operator

	// Path construction operators
	case "m":
		if len(op.operands) >= 2 {
			x, y := toFloat(op.operands[0]), toFloat(op.operands[1])
			ctx.moveTo(x, y)
		}
	case "l":
		if len(op.operands) >= 2 {
			x, y := toFloat(op.operands[0]), toFloat(op.operands[1])
			ctx.lineTo(x, y)
		}
	case "c":
		if len(op.operands) >= 6 {
			x1 := toFloat(op.operands[0])
			y1 := toFloat(op.operands[1])
			x2 := toFloat(op.operands[2])
			y2 := toFloat(op.operands[3])
			x3 := toFloat(op.operands[4])
			y3 := toFloat(op.operands[5])
			ctx.cubicTo(x1, y1, x2, y2, x3, y3)
		}
	case "v":
		if len(op.operands) >= 4 {
			x2 := toFloat(op.operands[0])
			y2 := toFloat(op.operands[1])
			x3 := toFloat(op.operands[2])
			y3 := toFloat(op.operands[3])
			// Use current point as first control point
			cx, cy := ctx.currentPoint()
			ctx.cubicTo(cx, cy, x2, y2, x3, y3)
		}
	case "y":
		if len(op.operands) >= 4 {
			x1 := toFloat(op.operands[0])
			y1 := toFloat(op.operands[1])
			x3 := toFloat(op.operands[2])
			y3 := toFloat(op.operands[3])
			// Use end point as second control point
			ctx.cubicTo(x1, y1, x3, y3, x3, y3)
		}
	case "h":
		ctx.closePath()
	case "re":
		if len(op.operands) >= 4 {
			x := toFloat(op.operands[0])
			y := toFloat(op.operands[1])
			w := toFloat(op.operands[2])
			h := toFloat(op.operands[3])
			ctx.rectangle(x, y, w, h)
		}

	// Path painting operators
	case "S":
		ctx.stroke()
	case "s":
		ctx.closePath()
		ctx.stroke()
	case "f", "F":
		ctx.fill(windingRule)
	case "f*":
		ctx.fill(evenOddRule)
	case "B":
		ctx.fillStroke(windingRule)
	case "B*":
		ctx.fillStroke(evenOddRule)
	case "b":
		ctx.closePath()
		ctx.fillStroke(windingRule)
	case "b*":
		ctx.closePath()
		ctx.fillStroke(evenOddRule)
	case "n":
		ctx.newPath()

	// Clipping operators
	case "W":
		ctx.clip(windingRule)
	case "W*":
		ctx.clip(evenOddRule)

	// Color operators
	case "CS":
		// Set stroking color space
		if len(op.operands) >= 1 {
			if name, ok := op.operands[0].(string); ok {
				state.strokeColorSpace = name
			}
		}
	case "cs":
		// Set non-stroking color space
		if len(op.operands) >= 1 {
			if name, ok := op.operands[0].(string); ok {
				state.fillColorSpace = name
			}
		}
	case "SC", "SCN":
		// Set stroking color
		ctx.setStrokeColor(operandsToColor(op.operands, state.strokeColorSpace))
	case "sc", "scn":
		// Set non-stroking color
		ctx.setFillColor(operandsToColor(op.operands, state.fillColorSpace))
	case "G":
		if len(op.operands) >= 1 {
			g := toFloat(op.operands[0])
			ctx.setStrokeColor(grayToColor(g))
		}
	case "g":
		if len(op.operands) >= 1 {
			g := toFloat(op.operands[0])
			ctx.setFillColor(grayToColor(g))
		}
	case "RG":
		if len(op.operands) >= 3 {
			r := toFloat(op.operands[0])
			g := toFloat(op.operands[1])
			b := toFloat(op.operands[2])
			ctx.setStrokeColor(rgbToColor(r, g, b))
		}
	case "rg":
		if len(op.operands) >= 3 {
			r := toFloat(op.operands[0])
			g := toFloat(op.operands[1])
			b := toFloat(op.operands[2])
			ctx.setFillColor(rgbToColor(r, g, b))
		}
	case "K":
		if len(op.operands) >= 4 {
			c := toFloat(op.operands[0])
			m := toFloat(op.operands[1])
			y := toFloat(op.operands[2])
			k := toFloat(op.operands[3])
			ctx.setStrokeColor(cmykToColor(c, m, y, k))
		}
	case "k":
		if len(op.operands) >= 4 {
			c := toFloat(op.operands[0])
			m := toFloat(op.operands[1])
			y := toFloat(op.operands[2])
			k := toFloat(op.operands[3])
			ctx.setFillColor(cmykToColor(c, m, y, k))
		}

	// Text operators
	case "BT":
		state.textState.beginTextObject()
	case "ET":
		// End text object
	case "Tc":
		if len(op.operands) >= 1 {
			state.textState.charSpace = toFloat(op.operands[0])
		}
	case "Tw":
		if len(op.operands) >= 1 {
			state.textState.wordSpace = toFloat(op.operands[0])
		}
	case "Tz":
		if len(op.operands) >= 1 {
			state.textState.hScale = toFloat(op.operands[0]) / 100.0
		}
	case "TL":
		if len(op.operands) >= 1 {
			state.textState.leading = toFloat(op.operands[0])
		}
	case "Tf":
		if len(op.operands) >= 2 {
			if name, ok := op.operands[0].(string); ok {
				state.textState.fontName = name
				if debugTextRender && name == "TT5" {
					fmt.Printf("DEBUG: Selected font TT5, size=%.1f\n", toFloat(op.operands[1]))
				}
			}
			state.textState.fontSize = toFloat(op.operands[1])
		}
	case "Tr":
		if len(op.operands) >= 1 {
			state.textState.renderMode = int(toFloat(op.operands[0]))
		}
	case "Ts":
		if len(op.operands) >= 1 {
			state.textState.rise = toFloat(op.operands[0])
		}
	case "Td":
		if len(op.operands) >= 2 {
			tx := toFloat(op.operands[0])
			ty := toFloat(op.operands[1])
			state.textState.translateLine(tx, ty)
		}
	case "TD":
		if len(op.operands) >= 2 {
			tx := toFloat(op.operands[0])
			ty := toFloat(op.operands[1])
			state.textState.leading = -ty
			state.textState.translateLine(tx, ty)
		}
	case "Tm":
		if len(op.operands) >= 6 {
			a := toFloat(op.operands[0])
			b := toFloat(op.operands[1])
			c := toFloat(op.operands[2])
			d := toFloat(op.operands[3])
			e := toFloat(op.operands[4])
			f := toFloat(op.operands[5])
			state.textState.setMatrix(a, b, c, d, e, f)
		}
	case "T*":
		state.textState.nextLine()
	case "Tj":
		if len(op.operands) >= 1 {
			if s, ok := op.operands[0].(string); ok {
				if debugTextRender && state.textState.fontName == "TT5" {
					fmt.Printf("DEBUG: Tj with TT5: text=%q tm=[%.1f,%.1f,%.1f,%.1f,%.1f,%.1f]\n",
						s[:min(50, len(s))], state.textState.tm[0], state.textState.tm[1],
						state.textState.tm[2], state.textState.tm[3], state.textState.tm[4], state.textState.tm[5])
				}
				r.renderText(ctx, state, s, res)
			}
		}
	case "TJ":
		if len(op.operands) >= 1 {
			if arr, ok := op.operands[0].([]any); ok {
				for _, elem := range arr {
					switch v := elem.(type) {
					case string:
						r.renderText(ctx, state, v, res)
					case float64:
						// TJ values are in thousandths of text space units
						// Negative values = move right (reduce spacing)
						// adjustPosition handles the 1/1000 scaling internally
						state.textState.adjustPosition(v)
					case int:
						state.textState.adjustPosition(float64(v))
					}
				}
			}
		}
	case "'":
		// Move to next line and show text
		state.textState.nextLine()
		if len(op.operands) >= 1 {
			if s, ok := op.operands[0].(string); ok {
				r.renderText(ctx, state, s, res)
			}
		}
	case "\"":
		// Set word/char spacing, move to next line, show text
		if len(op.operands) >= 3 {
			state.textState.wordSpace = toFloat(op.operands[0])
			state.textState.charSpace = toFloat(op.operands[1])
			state.textState.nextLine()
			if s, ok := op.operands[2].(string); ok {
				r.renderText(ctx, state, s, res)
			}
		}

	// XObject operators
	case "Do":
		if len(op.operands) >= 1 {
			if name, ok := op.operands[0].(string); ok {
				r.renderXObject(ctx, state, name, res)
			}
		}

	// Inline image operators
	case "BI":
		// Begin inline image - handled by parser
	case "ID":
		// Image data - handled by parser
	case "EI":
		// End inline image - handled by parser

	// Marked content operators (ignore)
	case "BMC", "BDC", "EMC", "MP", "DP":
		// Marked content - ignore

	// Compatibility operators (ignore)
	case "BX", "EX":
		// Compatibility section - ignore
	}

	return nil
}

// debugTextRender controls whether to print debug info during text rendering.
var debugTextRender = false

// renderText renders text at the current text position.
func (r *Renderer) renderText(ctx *context, state *graphicsState, text string, res *resources) {
	ts := &state.textState

	// Get font from cache
	font := r.fontCache.getFont(r, res, ts.fontName)

	if debugTextRender && len(text) > 0 && len(text) < 50 {
		hasSfnt := font != nil && font.sfntFont != nil
		fmt.Printf("renderText: font=%s hasSfnt=%v text=%q fontSize=%.1f tm=[%.1f,%.1f,%.1f,%.1f,%.1f,%.1f]\n",
			ts.fontName, hasSfnt, text, ts.fontSize,
			ts.tm[0], ts.tm[1], ts.tm[2], ts.tm[3], ts.tm[4], ts.tm[5])
	}

	// Get base font size
	fontSize := ts.fontSize

	// Render based on render mode (3 = invisible)
	if ts.renderMode == 3 {
		// Still need to advance position for invisible text
		textWidth := r.calculateTextWidth(text, font, ts)
		ts.advancePosition(textWidth)
		return
	}

	// Calculate base position from text matrix
	// The text matrix transforms from text space to user space
	// We need to apply CTM to get to device space
	baseX := ts.tm[4]
	baseY := ts.tm[5]

	// Transform through CTM to get device coordinates
	deviceX, deviceY := ctx.transformPoint(baseX, baseY)

	// Get the current scale from combined matrices
	// Text matrix scale combined with CTM scale
	textScaleX := math.Sqrt(ts.tm[0]*ts.tm[0]+ts.tm[1]*ts.tm[1]) * ts.hScale
	textScaleY := math.Sqrt(ts.tm[2]*ts.tm[2] + ts.tm[3]*ts.tm[3])
	if textScaleY == 0 {
		textScaleY = textScaleX // Assume uniform scale
	}

	// Get CTM scale
	ctmScaleX := math.Sqrt(ctx.matrix[0]*ctx.matrix[0] + ctx.matrix[1]*ctx.matrix[1])
	ctmScaleY := math.Sqrt(ctx.matrix[2]*ctx.matrix[2] + ctx.matrix[3]*ctx.matrix[3])
	if ctmScaleY == 0 {
		ctmScaleY = ctmScaleX
	}

	// Combined scale for font size
	effectiveFontSize := fontSize * textScaleY * math.Abs(ctmScaleY)

	if debugTextRender && len(text) > 0 && text != " " {
		fmt.Printf("  -> devicePos=(%.1f,%.1f) textScale=(%.1f,%.1f) ctmScale=(%.1f,%.1f) effectiveSize=%.1f\n",
			deviceX, deviceY, textScaleX, textScaleY, ctmScaleX, ctmScaleY, effectiveFontSize)
	}

	// Position for rendering
	currentX := deviceX
	currentY := deviceY

	// Total width for position update
	totalWidth := 0.0

	// Render each character
	for i := 0; i < len(text); i++ {
		charCode := text[i]

		// Get character width in text space units (1/1000 of font size)
		var charWidth float64
		if font != nil {
			charWidth = font.getCharWidth(charCode)
		} else {
			charWidth = 600 // Default width
		}

		// Convert width to text space
		charWidthTS := charWidth / 1000.0 * fontSize

		// Skip rendering spaces (but still advance position)
		if charCode == ' ' {
			totalWidth += (charWidthTS + ts.wordSpace + ts.charSpace)
			currentX += (charWidthTS + ts.wordSpace + ts.charSpace) * textScaleX * ctmScaleX
			continue
		}

		// Try to render the glyph
		if font != nil && font.sfntFont != nil {
			glyphIndex, _, ok := font.getGlyph(charCode)
			if debugTextRender && ts.fontName == "TT5" && i < 5 {
				fmt.Printf("  TT5 glyph: char=%c (0x%02X) glyphIdx=%d ok=%v\n", charCode, charCode, glyphIndex, ok)
			}
			if ok && glyphIndex != 0 {
				// Render the glyph at current position
				font.renderGlyph(ctx.img, glyphIndex, currentX, currentY, effectiveFontSize, state.fillColor)
			}
			// Skip placeholder for now to measure actual glyph rendering quality
		}
		// Skip placeholder when font not available

		// Advance position
		advance := charWidthTS + ts.charSpace
		totalWidth += advance
		currentX += advance * textScaleX * ctmScaleX
	}

	// Update text state position
	ts.advancePosition(totalWidth)
}

// renderPlaceholderGlyph renders a simple rectangle as a placeholder for a glyph.
func (r *Renderer) renderPlaceholderGlyph(ctx *context, x, y, fontSize, width float64, col color.RGBA) {
	// Draw a simple filled rectangle as placeholder
	height := fontSize * 0.7

	// Adjust y position (baseline to top of glyph)
	rectY := y - height

	img := ctx.image()
	bounds := img.Bounds()

	// Draw the rectangle
	x1 := int(x)
	y1 := int(rectY)
	x2 := int(x + width*0.8)
	y2 := int(y)

	// Clamp to image bounds
	if x1 < bounds.Min.X {
		x1 = bounds.Min.X
	}
	if y1 < bounds.Min.Y {
		y1 = bounds.Min.Y
	}
	if x2 > bounds.Max.X {
		x2 = bounds.Max.X
	}
	if y2 > bounds.Max.Y {
		y2 = bounds.Max.Y
	}

	for py := y1; py < y2; py++ {
		for px := x1; px < x2; px++ {
			img.Set(px, py, col)
		}
	}
}

// calculateTextWidth calculates the total width of text in text space units.
func (r *Renderer) calculateTextWidth(text string, font *pdfFont, ts *textState) float64 {
	totalWidth := 0.0

	for i := 0; i < len(text); i++ {
		charCode := text[i]

		var charWidth float64
		if font != nil {
			charWidth = font.getCharWidth(charCode)
		} else {
			charWidth = 600
		}

		charWidthTS := charWidth / 1000.0 * ts.fontSize

		if charCode == ' ' {
			totalWidth += charWidthTS + ts.wordSpace + ts.charSpace
		} else {
			totalWidth += charWidthTS + ts.charSpace
		}
	}

	return totalWidth
}

// renderXObject renders an XObject (image or form).
func (r *Renderer) renderXObject(ctx *context, state *graphicsState, name string, res *resources) {
	if res.dict == nil {
		return
	}

	xobjDict, err := r.getDictEntry(res.dict, "XObject")
	if err != nil || xobjDict == nil {
		return
	}

	obj, found := xobjDict.Find(name)
	if !found {
		return
	}

	obj, err = r.ctx.Dereference(obj)
	if err != nil {
		return
	}

	sd, ok := obj.(types.StreamDict)
	if !ok {
		return
	}

	// Get subtype
	subtype, _ := sd.Dict.Find("Subtype")
	subtypeName, _ := subtype.(types.Name)

	switch subtypeName.String() {
	case "Image":
		r.renderImage(ctx, state, &sd)
	case "Form":
		r.renderForm(ctx, state, &sd, res)
	}
}

// renderImage renders an image XObject.
func (r *Renderer) renderImage(ctx *context, state *graphicsState, sd *types.StreamDict) {
	// Get image dimensions
	widthObj, _ := sd.Dict.Find("Width")
	heightObj, _ := sd.Dict.Find("Height")

	width := toIntFromObj(widthObj)
	height := toIntFromObj(heightObj)

	if width <= 0 || height <= 0 {
		if debugTextRender {
			fmt.Printf("DEBUG: Image skipped - invalid dimensions %dx%d\n", width, height)
		}
		return
	}

	// Check for JPEG2000 filter (JPXDecode) - handle before pdfcpu's Decode()
	filterObj, _ := sd.Dict.Find("Filter")
	if filterName, ok := filterObj.(types.Name); ok && filterName.String() == "JPXDecode" {
		// Use raw content for JPEG2000 (pdfcpu doesn't decode JPX)
		rawData := sd.Raw
		if len(rawData) == 0 {
			rawData = sd.Content
		}
		if len(rawData) > 0 {
			img, err := jpeg2000.Decode(bytes.NewReader(rawData))
			if err == nil {
				ctx.drawImage(img)
				return
			}
			// Fall through to try pdfcpu decode
			if debugTextRender {
				fmt.Printf("DEBUG: JPEG2000 decode failed: %v\n", err)
			}
		}
	}

	// Decode stream using pdfcpu
	if err := sd.Decode(); err != nil {
		if debugTextRender {
			fmt.Printf("DEBUG: Image decode failed: %v\n", err)
		}
		return
	}

	if debugTextRender {
		filterObj, _ := sd.Dict.Find("Filter")
		fmt.Printf("DEBUG: Rendering image %dx%d, filter=%v, data=%d bytes\n",
			width, height, filterObj, len(sd.Content))
	}

	// Get color space and bits per component
	bpcObj, _ := sd.Dict.Find("BitsPerComponent")
	bpc := toIntFromObj(bpcObj)
	if bpc == 0 {
		bpc = 8
	}

	csObj, _ := sd.Dict.Find("ColorSpace")
	cs := "DeviceRGB"
	if csName, ok := csObj.(types.Name); ok {
		cs = csName.String()
	}

	// Check if we have enough data for raw pixels
	expectedSize := width * height
	switch cs {
	case "DeviceRGB":
		expectedSize *= 3
	case "DeviceCMYK":
		expectedSize *= 4
	}

	if len(sd.Content) < expectedSize {
		if debugTextRender {
			fmt.Printf("DEBUG: Image data too small: have %d, need %d - likely unhandled filter\n",
				len(sd.Content), expectedSize)
		}
		return
	}

	// Create image
	img := r.decodeImage(sd.Content, width, height, bpc, cs)
	if img == nil {
		if debugTextRender {
			fmt.Printf("DEBUG: Image decode returned nil\n")
		}
		return
	}

	// Draw image in unit square (CTM handles positioning)
	ctx.drawImage(img)
}

// decodeImage decodes image data to an image.Image.
func (r *Renderer) decodeImage(data []byte, width, height, bpc int, colorSpace string) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	switch colorSpace {
	case "DeviceRGB":
		if bpc == 8 && len(data) >= width*height*3 {
			for y := range height {
				for x := range width {
					i := (y*width + x) * 3
					if i+2 < len(data) {
						img.Set(x, y, color.RGBA{data[i], data[i+1], data[i+2], 255})
					}
				}
			}
		}
	case "DeviceGray":
		if bpc == 8 && len(data) >= width*height {
			for y := range height {
				for x := range width {
					i := y*width + x
					if i < len(data) {
						g := data[i]
						img.Set(x, y, color.RGBA{g, g, g, 255})
					}
				}
			}
		}
	case "DeviceCMYK":
		if bpc == 8 && len(data) >= width*height*4 {
			for y := range height {
				for x := range width {
					i := (y*width + x) * 4
					if i+3 < len(data) {
						c, m, yk, k := data[i], data[i+1], data[i+2], data[i+3]
						r, g, b := cmykToRGB(float64(c)/255, float64(m)/255, float64(yk)/255, float64(k)/255)
						img.Set(x, y, color.RGBA{r, g, b, 255})
					}
				}
			}
		}
	}

	return img
}

// renderForm renders a form XObject.
func (r *Renderer) renderForm(ctx *context, state *graphicsState, sd *types.StreamDict, parentRes *resources) {
	// Decode stream
	if err := sd.Decode(); err != nil {
		return
	}

	ctx.push()

	// Apply form matrix if present
	if matrixObj, found := sd.Dict.Find("Matrix"); found {
		if arr, ok := matrixObj.(types.Array); ok && len(arr) == 6 {
			a := toFloatFromObj(arr[0])
			b := toFloatFromObj(arr[1])
			c := toFloatFromObj(arr[2])
			d := toFloatFromObj(arr[3])
			e := toFloatFromObj(arr[4])
			f := toFloatFromObj(arr[5])
			ctx.transform(a, b, c, d, e, f)
		}
	}

	// Get form resources or use parent resources
	formRes := parentRes
	if resObj, found := sd.Dict.Find("Resources"); found {
		resObj, _ = r.ctx.Dereference(resObj)
		if resDict, ok := resObj.(types.Dict); ok {
			formRes = &resources{
				ctx:    r.ctx,
				dict:   resDict,
				fonts:  make(map[string]*fontInfo),
				images: make(map[string]*imageInfo),
			}
		}
	}

	// Render form content
	r.renderContentStream(ctx, sd.Content, formRes)

	ctx.pop()
}

// Helper functions

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

func toFloatFromObj(obj types.Object) float64 {
	switch v := obj.(type) {
	case types.Float:
		return float64(v)
	case types.Integer:
		return float64(v)
	default:
		return 0
	}
}

func toIntFromObj(obj types.Object) int {
	switch v := obj.(type) {
	case types.Integer:
		return int(v)
	case types.Float:
		return int(v)
	default:
		return 0
	}
}

func grayToColor(g float64) color.RGBA {
	v := uint8(g * 255)
	return color.RGBA{v, v, v, 255}
}

func rgbToColor(r, g, b float64) color.RGBA {
	return color.RGBA{
		R: uint8(r * 255),
		G: uint8(g * 255),
		B: uint8(b * 255),
		A: 255,
	}
}

func cmykToColor(c, m, y, k float64) color.RGBA {
	r, g, b := cmykToRGB(c, m, y, k)
	return color.RGBA{r, g, b, 255}
}

func cmykToRGB(c, m, y, k float64) (uint8, uint8, uint8) {
	r := (1 - c) * (1 - k)
	g := (1 - m) * (1 - k)
	b := (1 - y) * (1 - k)
	return uint8(r * 255), uint8(g * 255), uint8(b * 255)
}

func operandsToColor(operands []any, colorSpace string) color.RGBA {
	switch strings.ToLower(colorSpace) {
	case "devicergb", "/devicergb":
		if len(operands) >= 3 {
			return rgbToColor(toFloat(operands[0]), toFloat(operands[1]), toFloat(operands[2]))
		}
	case "devicecmyk", "/devicecmyk":
		if len(operands) >= 4 {
			return cmykToColor(toFloat(operands[0]), toFloat(operands[1]), toFloat(operands[2]), toFloat(operands[3]))
		}
	case "devicegray", "/devicegray":
		if len(operands) >= 1 {
			return grayToColor(toFloat(operands[0]))
		}
	default:
		// Default to RGB if we have 3+ components
		if len(operands) >= 3 {
			return rgbToColor(toFloat(operands[0]), toFloat(operands[1]), toFloat(operands[2]))
		}
		if len(operands) >= 1 {
			return grayToColor(toFloat(operands[0]))
		}
	}
	return color.RGBA{0, 0, 0, 255}
}

// decodeText attempts to decode a PDF text string.
func decodeText(text string, fontName string, res *resources) string {
	// Handle UTF-16BE encoding (common in PDF)
	if len(text) >= 2 && text[0] == 0xFE && text[1] == 0xFF {
		// UTF-16BE BOM
		u16 := make([]uint16, 0, len(text)/2)
		for i := 2; i+1 < len(text); i += 2 {
			u16 = append(u16, uint16(text[i])<<8|uint16(text[i+1]))
		}
		return string(utf16.Decode(u16))
	}

	// For other encodings, try direct interpretation
	// A full implementation would use the font's encoding dictionary
	return text
}
