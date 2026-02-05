package render

import (
	"image/color"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// textState tracks PDF text state including positioning and rendering parameters.
type textState struct {
	charSpace  float64    // Tc - character spacing
	wordSpace  float64    // Tw - word spacing
	hScale     float64    // Tz - horizontal scaling (1.0 = 100%)
	leading    float64    // TL - text leading
	fontName   string     // Current font name
	fontSize   float64    // Current font size
	renderMode int        // Tr - text rendering mode (0=fill, 1=stroke, 2=fill+stroke, 3=invisible, etc.)
	rise       float64    // Ts - text rise
	tm         [6]float64 // Text matrix (a, b, c, d, e, f)
	tlm        [6]float64 // Text line matrix
}

// reset resets the text state to default values (for initial state).
// This resets ALL text parameters including font.
func (ts *textState) reset() {
	ts.charSpace = 0
	ts.wordSpace = 0
	ts.hScale = 1.0
	ts.leading = 0
	ts.fontName = ""
	ts.fontSize = 0
	ts.renderMode = 0
	ts.rise = 0
	ts.tm = [6]float64{1, 0, 0, 1, 0, 0}  // Identity matrix
	ts.tlm = [6]float64{1, 0, 0, 1, 0, 0}
}

// beginTextObject resets only the text matrices for a new text object (BT operator).
// Per PDF spec, text parameters (font, spacing, etc.) persist across text objects,
// but the text matrix is reset to identity.
func (ts *textState) beginTextObject() {
	ts.tm = [6]float64{1, 0, 0, 1, 0, 0}  // Identity matrix
	ts.tlm = [6]float64{1, 0, 0, 1, 0, 0}
}

// setMatrix sets both the text matrix (Tm) and text line matrix (Tlm).
// This is used by the Tm operator.
func (ts *textState) setMatrix(a, b, c, d, e, f float64) {
	ts.tm = [6]float64{a, b, c, d, e, f}
	ts.tlm = [6]float64{a, b, c, d, e, f}
}

// translateLine moves the text line matrix by (tx, ty) and updates the text matrix.
// This implements the Td operator: move to start of next line with offset (tx, ty).
// Per PDF spec: Tlm = Tlm × [1 0 0 1 tx ty]
func (ts *textState) translateLine(tx, ty float64) {
	// Matrix multiplication: Tlm × T(tx, ty)
	// For translation matrix [1 0 0 1 tx ty]:
	// - a, b, c, d remain unchanged
	// - new_e = a*tx + c*ty + e
	// - new_f = b*tx + d*ty + f
	newE := ts.tlm[0]*tx + ts.tlm[2]*ty + ts.tlm[4]
	newF := ts.tlm[1]*tx + ts.tlm[3]*ty + ts.tlm[5]
	ts.tlm[4] = newE
	ts.tlm[5] = newF
	ts.tm = ts.tlm
}

// nextLine moves to the start of the next line using the current leading.
// This implements the T* operator: equivalent to Td(0, -TL).
func (ts *textState) nextLine() {
	ts.translateLine(0, -ts.leading)
}

// getPosition returns the current text position (x, y) from the text matrix.
func (ts *textState) getPosition() (x, y float64) {
	return ts.tm[4], ts.tm[5]
}

// advancePosition advances the text position horizontally by the given width.
// This is called after rendering text to update position for next glyph.
func (ts *textState) advancePosition(width float64) {
	// Account for horizontal scaling, character spacing, and word spacing
	advance := (width*ts.fontSize + ts.charSpace) * ts.hScale

	// Update text matrix translation
	ts.tm[4] += advance * ts.tm[0] // x += advance * scale_x
	ts.tm[5] += advance * ts.tm[1] // y += advance * skew_y
}

// adjustPosition adjusts text position by the given amount (in thousandths of text space units).
// This implements the numeric adjustments in TJ operator arrays.
// Negative values move right, positive values move left.
func (ts *textState) adjustPosition(amount float64) {
	// TJ adjustment is in thousandths of a unit of text space
	adjustment := -(amount / 1000.0) * ts.fontSize * ts.hScale

	ts.tm[4] += adjustment * ts.tm[0]
	ts.tm[5] += adjustment * ts.tm[1]
}

// graphicsState represents the PDF graphics state including colors, line styles,
// and transformation matrices.
type graphicsState struct {
	// Color state
	fillColor        color.RGBA
	strokeColor      color.RGBA
	fillColorSpace   string // e.g., "DeviceRGB", "DeviceGray", "DeviceCMYK"
	strokeColorSpace string

	// Line style state
	lineWidth   float64
	lineCap     int // 0=butt, 1=round, 2=square
	lineJoin    int // 0=miter, 1=round, 2=bevel
	miterLimit  float64
	dashPattern []float64
	dashPhase   float64

	// Transformation state
	ctm [6]float64 // Current transformation matrix (a, b, c, d, e, f)

	// Text state
	textState textState
}

// newGraphicsState creates a new graphics state with default values.
func newGraphicsState() *graphicsState {
	gs := &graphicsState{
		fillColor:        color.RGBA{R: 0, G: 0, B: 0, A: 255},       // Black
		strokeColor:      color.RGBA{R: 0, G: 0, B: 0, A: 255},       // Black
		fillColorSpace:   "DeviceRGB",
		strokeColorSpace: "DeviceRGB",
		lineWidth:        1.0,
		lineCap:          0, // Butt cap
		lineJoin:         0, // Miter join
		miterLimit:       10.0,
		dashPattern:      nil, // Solid line (no dash)
		dashPhase:        0,
		ctm:              [6]float64{1, 0, 0, 1, 0, 0}, // Identity matrix
	}

	gs.textState.reset()

	return gs
}

// clone creates a deep copy of the graphics state.
// This is used for save/restore operations (q/Q operators).
func (gs *graphicsState) clone() *graphicsState {
	clone := &graphicsState{
		fillColor:        gs.fillColor,
		strokeColor:      gs.strokeColor,
		fillColorSpace:   gs.fillColorSpace,
		strokeColorSpace: gs.strokeColorSpace,
		lineWidth:        gs.lineWidth,
		lineCap:          gs.lineCap,
		lineJoin:         gs.lineJoin,
		miterLimit:       gs.miterLimit,
		dashPhase:        gs.dashPhase,
		ctm:              gs.ctm,
		textState:        gs.textState,
	}

	// Deep copy dash pattern
	if gs.dashPattern != nil {
		clone.dashPattern = make([]float64, len(gs.dashPattern))
		copy(clone.dashPattern, gs.dashPattern)
	}

	return clone
}

// fontInfo holds information about a font resource.
type fontInfo struct {
	name     string     // Font name (e.g., "Helvetica")
	subtype  string     // Font subtype (Type1, TrueType, etc.)
	encoding string     // Encoding name
	dict     types.Dict // Font dictionary
	// Additional fields will be added as needed for glyph metrics, etc.
}

// imageInfo holds information about an image resource.
type imageInfo struct {
	width      int        // Image width in pixels
	height     int        // Image height in pixels
	colorSpace string     // Color space
	bpc        int        // Bits per component
	dict       types.Dict // Image dictionary
	// Additional fields will be added for image data access
}

// resources holds the resources available for rendering a page.
type resources struct {
	ctx    *model.Context        // PDF context for accessing objects
	dict   types.Dict            // Resources dictionary from page
	fonts  map[string]*fontInfo  // Font resources keyed by name
	images map[string]*imageInfo // Image resources keyed by name
	// Additional resource types (XObjects, ColorSpaces, etc.) will be added as needed
}

// newResources creates a new resources structure.
func newResources(ctx *model.Context, dict types.Dict) *resources {
	return &resources{
		ctx:    ctx,
		dict:   dict,
		fonts:  make(map[string]*fontInfo),
		images: make(map[string]*imageInfo),
	}
}

// Note: Fonts are loaded via fontCache.getFont() in font.go, which handles
// lazy loading from the resources dictionary. The resources.fonts map tracks
// available font names but actual pdfFont objects are cached in fontCache.

// Note: Images (XObjects) are loaded directly in renderXObject() in render.go,
// which accesses the XObject dictionary and renders images/forms. The
// resources.images map tracks available XObject names for reference.
