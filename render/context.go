package render

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// Fill rules for path filling
const (
	windingRule = iota // Non-zero winding number rule
	evenOddRule        // Even-odd rule
)

// Line cap styles
const (
	capButt   = 0
	capRound  = 1
	capSquare = 2
)

// Line join styles
const (
	joinMiter = 0
	joinRound = 1
	joinBevel = 2
)

// pathSegment represents a segment in a path
type pathSegment struct {
	op     pathOp
	points []point
}

type pathOp int

const (
	opMoveTo pathOp = iota
	opLineTo
	opCubicTo
	opClose
)

type point struct {
	x, y float64
}

// context represents a 2D graphics rendering context
type context struct {
	img          *image.RGBA
	stateStack   []*graphicsState
	currentState *graphicsState

	// Path state (separate from graphics state for proper PDF semantics)
	path            []pathSegment
	currentX        float64
	currentY        float64
	hasCurrentPoint bool
	clipPath        []pathSegment

	// Transformation matrix
	matrix [6]float64
}

// newContext creates a new graphics context with the given dimensions
func newContext(width, height int) *context {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Fill with white background (PDF pages are white by default)
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for y := range height {
		for x := range width {
			img.Set(x, y, white)
		}
	}

	ctx := &context{
		img:          img,
		currentState: newGraphicsState(),
		matrix:       [6]float64{1, 0, 0, 1, 0, 0}, // Identity matrix
	}

	return ctx
}

// Width returns the width of the context's image
func (ctx *context) Width() int {
	return ctx.img.Bounds().Dx()
}

// Height returns the height of the context's image
func (ctx *context) Height() int {
	return ctx.img.Bounds().Dy()
}

// image returns the underlying image
func (ctx *context) image() *image.RGBA {
	return ctx.img
}

// state returns the current graphics state
func (ctx *context) state() *graphicsState {
	return ctx.currentState
}

// push saves the current graphics state onto the stack
func (ctx *context) push() {
	// Clone the current graphics state
	stateCopy := ctx.currentState.clone()

	// Also save transformation matrix
	stateCopy.ctm = ctx.matrix

	ctx.stateStack = append(ctx.stateStack, stateCopy)
}

// pop restores the most recently saved graphics state
func (ctx *context) pop() {
	if len(ctx.stateStack) == 0 {
		return
	}

	ctx.currentState = ctx.stateStack[len(ctx.stateStack)-1]
	ctx.matrix = ctx.currentState.ctm
	ctx.stateStack = ctx.stateStack[:len(ctx.stateStack)-1]
}

// Transformation methods

// translate applies a translation transformation
func (ctx *context) translate(x, y float64) {
	ctx.transform(1, 0, 0, 1, x, y)
}

// scale applies a scaling transformation
func (ctx *context) scale(sx, sy float64) {
	ctx.transform(sx, 0, 0, sy, 0, 0)
}

// transform applies an affine transformation matrix
func (ctx *context) transform(a, b, c, d, e, f float64) {
	m := ctx.matrix
	ctx.matrix = [6]float64{
		m[0]*a + m[2]*b,
		m[1]*a + m[3]*b,
		m[0]*c + m[2]*d,
		m[1]*c + m[3]*d,
		m[0]*e + m[2]*f + m[4],
		m[1]*e + m[3]*f + m[5],
	}
}

// transformPoint applies the current transformation matrix to a point
func (ctx *context) transformPoint(x, y float64) (float64, float64) {
	m := ctx.matrix
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// Path construction methods

// newPath clears the current path
func (ctx *context) newPath() {
	ctx.path = nil
	ctx.hasCurrentPoint = false
}

// moveTo starts a new subpath at the given point
func (ctx *context) moveTo(x, y float64) {
	tx, ty := ctx.transformPoint(x, y)
	ctx.path = append(ctx.path, pathSegment{
		op:     opMoveTo,
		points: []point{{tx, ty}},
	})
	ctx.currentX = tx
	ctx.currentY = ty
	ctx.hasCurrentPoint = true
}

// lineTo adds a line segment to the current path
func (ctx *context) lineTo(x, y float64) {
	if !ctx.hasCurrentPoint {
		ctx.moveTo(x, y)
		return
	}

	tx, ty := ctx.transformPoint(x, y)
	ctx.path = append(ctx.path, pathSegment{
		op:     opLineTo,
		points: []point{{tx, ty}},
	})
	ctx.currentX = tx
	ctx.currentY = ty
}

// cubicTo adds a cubic Bézier curve to the current path
func (ctx *context) cubicTo(x1, y1, x2, y2, x3, y3 float64) {
	if !ctx.hasCurrentPoint {
		ctx.moveTo(x1, y1)
	}

	tx1, ty1 := ctx.transformPoint(x1, y1)
	tx2, ty2 := ctx.transformPoint(x2, y2)
	tx3, ty3 := ctx.transformPoint(x3, y3)

	ctx.path = append(ctx.path, pathSegment{
		op: opCubicTo,
		points: []point{
			{tx1, ty1},
			{tx2, ty2},
			{tx3, ty3},
		},
	})
	ctx.currentX = tx3
	ctx.currentY = ty3
}

// closePath closes the current subpath
func (ctx *context) closePath() {
	if !ctx.hasCurrentPoint {
		return
	}

	ctx.path = append(ctx.path, pathSegment{
		op: opClose,
	})
}

// rectangle adds a rectangle to the current path
func (ctx *context) rectangle(x, y, w, h float64) {
	ctx.moveTo(x, y)
	ctx.lineTo(x+w, y)
	ctx.lineTo(x+w, y+h)
	ctx.lineTo(x, y+h)
	ctx.closePath()
}

// currentPoint returns the current point in the path
func (ctx *context) currentPoint() (float64, float64) {
	return ctx.currentX, ctx.currentY
}

// Color methods

// setFillColor sets the fill color
func (ctx *context) setFillColor(c color.RGBA) {
	ctx.currentState.fillColor = c
}

// setStrokeColor sets the stroke color
func (ctx *context) setStrokeColor(c color.RGBA) {
	ctx.currentState.strokeColor = c
}

// Line style methods

// setLineWidth sets the line width for stroking
func (ctx *context) setLineWidth(width float64) {
	ctx.currentState.lineWidth = width
}

// setLineCap sets the line cap style
func (ctx *context) setLineCap(cap int) {
	ctx.currentState.lineCap = cap
}

// setLineJoin sets the line join style
func (ctx *context) setLineJoin(join int) {
	ctx.currentState.lineJoin = join
}

// setMiterLimit sets the miter limit for line joins
func (ctx *context) setMiterLimit(limit float64) {
	ctx.currentState.miterLimit = limit
}

// setDash sets the dash pattern
func (ctx *context) setDash(dashArray []float64, dashOffset float64) {
	if len(dashArray) > 0 {
		ctx.currentState.dashPattern = make([]float64, len(dashArray))
		copy(ctx.currentState.dashPattern, dashArray)
	} else {
		ctx.currentState.dashPattern = nil
	}
	ctx.currentState.dashPhase = dashOffset
}

// Path painting methods

// fill fills the current path using the specified fill rule
func (ctx *context) fill(fillRule int) {
	if len(ctx.path) == 0 {
		return
	}

	// Convert path to polygon(s)
	polygons := ctx.pathToPolygons()

	// Apply clipping if set
	if len(ctx.clipPath) > 0 {
		polygons = ctx.clipPolygons(polygons)
	}

	// Rasterize and fill
	for _, poly := range polygons {
		ctx.fillPolygon(poly, ctx.currentState.fillColor, fillRule)
	}
}

// stroke strokes the current path
func (ctx *context) stroke() {
	if len(ctx.path) == 0 {
		return
	}

	// Convert path to stroked polygon
	polygons := ctx.strokePath()

	// Apply clipping if set
	if len(ctx.clipPath) > 0 {
		polygons = ctx.clipPolygons(polygons)
	}

	// Rasterize
	for _, poly := range polygons {
		ctx.fillPolygon(poly, ctx.currentState.strokeColor, windingRule)
	}
}

// fillStroke fills and then strokes the current path
func (ctx *context) fillStroke(fillRule int) {
	ctx.fill(fillRule)
	ctx.stroke()
}

// clip sets the current path as the clipping path
func (ctx *context) clip(fillRule int) {
	if len(ctx.path) == 0 {
		return
	}

	// Copy current path to clip path
	// Note: fillRule is stored but not yet used in clipping algorithm
	_ = fillRule
	ctx.clipPath = make([]pathSegment, len(ctx.path))
	for i, seg := range ctx.path {
		ctx.clipPath[i].op = seg.op
		ctx.clipPath[i].points = make([]point, len(seg.points))
		copy(ctx.clipPath[i].points, seg.points)
	}
}

// drawImage draws an image using the current transformation matrix
func (ctx *context) drawImage(img image.Image) {
	// The CTM should already position and scale the image appropriately
	// In PDF, images are placed in a 1x1 unit square and the CTM scales them

	// Get the transformed corners of the unit square (image space)
	x0, y0 := ctx.transformPoint(0, 0)
	x1, y1 := ctx.transformPoint(1, 1)

	// Calculate destination bounds
	bounds := image.Rect(
		int(math.Min(x0, x1)),
		int(math.Min(y0, y1)),
		int(math.Max(x0, x1)),
		int(math.Max(y0, y1)),
	)

	srcBounds := img.Bounds()
	println("DEBUG drawImage: src=", srcBounds.Dx(), "x", srcBounds.Dy(), "dst=", bounds.Dx(), "x", bounds.Dy(), "at", bounds.Min.X, ",", bounds.Min.Y)

	// Check source pixel at (200,75) if it's a header image
	if srcBounds.Dx() == 1275 && srcBounds.Dy() == 150 {
		if rgba, ok := img.(*image.RGBA); ok {
			for _, srcY := range []int{73, 74, 75, 76} {
				idx := rgba.PixOffset(200, srcY)
				println("DEBUG drawImage: src pixel (200,", srcY, ")=", rgba.Pix[idx], rgba.Pix[idx+1], rgba.Pix[idx+2], rgba.Pix[idx+3])
			}
		}
	}

	// Draw with clipping - scale the source image to fit the bounds
	draw.Draw(ctx.img, bounds, img, image.Point{}, draw.Over)

	// Check destination after draw
	if srcBounds.Dx() == 1275 && srcBounds.Dy() == 150 {
		dstX := bounds.Min.X + 200
		dstY := bounds.Min.Y + 75
		if dstX < ctx.img.Bounds().Dx() && dstY < ctx.img.Bounds().Dy() {
			idx := ctx.img.PixOffset(dstX, dstY)
			println("DEBUG drawImage: dst pixel (", dstX, ",", dstY, ")=", ctx.img.Pix[idx], ctx.img.Pix[idx+1], ctx.img.Pix[idx+2], ctx.img.Pix[idx+3])
		}
	}
}

// Helper methods for path operations

// pathToPolygons converts the current path to a set of polygons
func (ctx *context) pathToPolygons() [][]point {
	var polygons [][]point
	var currentPoly []point
	var startPoint point

	for _, seg := range ctx.path {
		switch seg.op {
		case opMoveTo:
			if len(currentPoly) > 0 {
				polygons = append(polygons, currentPoly)
			}
			currentPoly = []point{seg.points[0]}
			startPoint = seg.points[0]

		case opLineTo:
			currentPoly = append(currentPoly, seg.points[0])

		case opCubicTo:
			// Flatten cubic Bézier curve
			var lastPoint point
			if len(currentPoly) > 0 {
				lastPoint = currentPoly[len(currentPoly)-1]
			}
			points := ctx.flattenCubic(
				lastPoint,
				seg.points[0],
				seg.points[1],
				seg.points[2],
			)
			currentPoly = append(currentPoly, points...)

		case opClose:
			if len(currentPoly) > 0 {
				currentPoly = append(currentPoly, startPoint)
			}
		}
	}

	if len(currentPoly) > 0 {
		polygons = append(polygons, currentPoly)
	}

	return polygons
}

// flattenCubic approximates a cubic Bézier curve with line segments
func (ctx *context) flattenCubic(p0, p1, p2, p3 point) []point {
	var points []point

	// Simple recursive subdivision
	const tolerance = 0.5

	var flatten func(p0, p1, p2, p3 point, depth int)
	flatten = func(p0, p1, p2, p3 point, depth int) {
		if depth > 10 {
			points = append(points, p3)
			return
		}

		// Check if curve is flat enough
		d1 := ctx.pointLineDistance(p1, p0, p3)
		d2 := ctx.pointLineDistance(p2, p0, p3)

		if d1+d2 < tolerance {
			points = append(points, p3)
			return
		}

		// Subdivide using de Casteljau's algorithm
		p01 := point{(p0.x + p1.x) / 2, (p0.y + p1.y) / 2}
		p12 := point{(p1.x + p2.x) / 2, (p1.y + p2.y) / 2}
		p23 := point{(p2.x + p3.x) / 2, (p2.y + p3.y) / 2}
		p012 := point{(p01.x + p12.x) / 2, (p01.y + p12.y) / 2}
		p123 := point{(p12.x + p23.x) / 2, (p12.y + p23.y) / 2}
		p0123 := point{(p012.x + p123.x) / 2, (p012.y + p123.y) / 2}

		flatten(p0, p01, p012, p0123, depth+1)
		flatten(p0123, p123, p23, p3, depth+1)
	}

	flatten(p0, p1, p2, p3, 0)
	return points
}

// pointLineDistance calculates the distance from a point to a line segment
func (ctx *context) pointLineDistance(p, a, b point) float64 {
	dx := b.x - a.x
	dy := b.y - a.y

	if dx == 0 && dy == 0 {
		return math.Sqrt((p.x-a.x)*(p.x-a.x) + (p.y-a.y)*(p.y-a.y))
	}

	t := ((p.x-a.x)*dx + (p.y-a.y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))

	px := a.x + t*dx
	py := a.y + t*dy

	return math.Sqrt((p.x-px)*(p.x-px) + (p.y-py)*(p.y-py))
}

// fillPolygon fills a polygon using scanline algorithm
func (ctx *context) fillPolygon(poly []point, col color.Color, fillRule int) {
	if len(poly) < 3 {
		return
	}

	// Find bounding box
	minY, maxY := poly[0].y, poly[0].y
	for _, p := range poly {
		if p.y < minY {
			minY = p.y
		}
		if p.y > maxY {
			maxY = p.y
		}
	}

	// Scanline fill
	for y := int(minY); y <= int(maxY); y++ {
		intersections := ctx.findIntersections(poly, float64(y))

		// Sort intersections
		for i := range intersections {
			for j := i + 1; j < len(intersections); j++ {
				if intersections[j] < intersections[i] {
					intersections[i], intersections[j] = intersections[j], intersections[i]
				}
			}
		}

		// Fill between pairs
		if fillRule == windingRule {
			ctx.fillScanlineWinding(intersections, y, col)
		} else {
			ctx.fillScanlineEvenOdd(intersections, y, col)
		}
	}
}

// findIntersections finds x-coordinates where scanline intersects polygon edges
func (ctx *context) findIntersections(poly []point, y float64) []float64 {
	var intersections []float64

	for i := range poly {
		j := (i + 1) % len(poly)
		p1, p2 := poly[i], poly[j]

		if (p1.y <= y && p2.y > y) || (p2.y <= y && p1.y > y) {
			x := p1.x + (y-p1.y)*(p2.x-p1.x)/(p2.y-p1.y)
			intersections = append(intersections, x)
		}
	}

	return intersections
}

// fillScanlineEvenOdd fills a scanline using even-odd rule
func (ctx *context) fillScanlineEvenOdd(intersections []float64, y int, col color.Color) {
	for i := 0; i+1 < len(intersections); i += 2 {
		x1 := int(intersections[i])
		x2 := int(intersections[i+1])

		for x := x1; x <= x2; x++ {
			if x >= 0 && x < ctx.Width() && y >= 0 && y < ctx.Height() {
				ctx.img.Set(x, y, col)
			}
		}
	}
}

// fillScanlineWinding fills a scanline using winding rule (simplified)
func (ctx *context) fillScanlineWinding(intersections []float64, y int, col color.Color) {
	// Simplified: treat as even-odd for now
	// A proper implementation would track winding numbers
	ctx.fillScanlineEvenOdd(intersections, y, col)
}

// strokePath converts the current path to stroked polygons
func (ctx *context) strokePath() [][]point {
	var polygons [][]point

	width := ctx.currentState.lineWidth
	halfWidth := width / 2

	for i, seg := range ctx.path {
		if seg.op == opMoveTo {
			continue
		}

		// Get previous point
		var p0 point
		if i > 0 {
			prevSeg := ctx.path[i-1]
			if len(prevSeg.points) > 0 {
				p0 = prevSeg.points[len(prevSeg.points)-1]
			}
		}

		switch seg.op {
		case opLineTo:
			p1 := seg.points[0]
			poly := ctx.strokeLine(p0, p1, halfWidth)
			polygons = append(polygons, poly)

		case opCubicTo:
			// Flatten and stroke
			points := ctx.flattenCubic(p0, seg.points[0], seg.points[1], seg.points[2])
			prev := p0
			for _, p := range points {
				poly := ctx.strokeLine(prev, p, halfWidth)
				polygons = append(polygons, poly)
				prev = p
			}
		}
	}

	return polygons
}

// strokeLine creates a polygon for a stroked line segment
func (ctx *context) strokeLine(p0, p1 point, halfWidth float64) []point {
	dx := p1.x - p0.x
	dy := p1.y - p0.y
	length := math.Sqrt(dx*dx + dy*dy)

	if length == 0 {
		return nil
	}

	// Perpendicular vector
	nx := -dy / length * halfWidth
	ny := dx / length * halfWidth

	return []point{
		{p0.x + nx, p0.y + ny},
		{p1.x + nx, p1.y + ny},
		{p1.x - nx, p1.y - ny},
		{p0.x - nx, p0.y - ny},
	}
}

// clipPolygons clips polygons against the current clipping path
func (ctx *context) clipPolygons(polygons [][]point) [][]point {
	// Simplified: return as-is
	// A proper implementation would use Sutherland-Hodgman or similar
	return polygons
}
