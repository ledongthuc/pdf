// xobject_text_test.go — regression suite for Form XObject text extraction.
//
// Upstream issue: ledongthuc/pdf #67 — text in XObject form streams silently
// skipped by GetPlainText.
//
// Coverage:
//   - XObject1  Content().Text extracts text from a single-level Form XObject
//   - XObject2  GetPlainText extracts text from a single-level Form XObject
//   - XObject3  GetPlainText extracts text from a two-level nested XObject
//   - XObject4  Font from XObject's own /Resources/Font is used (not page fonts)
//   - XObject5  Depth beyond xobjMaxDepth: no panic, no hang; text silently absent
//
// Helper functions buildPDFFromObjects and formXObjStream are defined in
// page_test.go (same package); they are NOT redeclared here.
package pdf

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestXObjectContentText verifies that Content().Text correctly recurses into
// a Form XObject and returns the XObject's text ("XObjText") one rune at a
// time. Content().Text emits one Text entry per decoded rune, so the S fields
// must be joined before the string-contains check.
func TestXObjectContentText(t *testing.T) {
	const want = "XObjText"
	xobjBody := fmt.Sprintf("BT (%s) Tj ET", want)
	pageContent := "/Fm0 Do"

	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page — references Form XObject /Fm0 and content stream
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm0 5 0 R >> >> /Contents 4 0 R >>",
		// 4: Page content stream — invokes the XObject
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		// 5: Form XObject with the target text
		formXObjStream(xobjBody, ""),
	})

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}

	texts := r.Page(1).Content().Text
	var got strings.Builder
	for _, tx := range texts {
		got.WriteString(tx.S)
	}
	if !strings.Contains(got.String(), want) {
		t.Errorf("Content().Text joined = %q; want it to contain %q", got.String(), want)
	}
}

// TestXObjectGetPlainText verifies that GetPlainText correctly recurses into
// a Form XObject and extracts the XObject's text string ("XObjText").
func TestXObjectGetPlainText(t *testing.T) {
	const want = "XObjText"
	xobjBody := fmt.Sprintf("BT (%s) Tj ET", want)
	pageContent := "/Fm0 Do"

	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm0 5 0 R >> >> /Contents 4 0 R >>",
		// 4: Page content
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		// 5: Form XObject
		formXObjStream(xobjBody, ""),
	})

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(got, want) {
		t.Errorf("GetPlainText = %q; want it to contain %q", got, want)
	}
}

// TestXObjectNestedRecursion verifies that GetPlainText recurses through two
// levels of Form XObject nesting and still extracts the innermost text
// ("XObjText").  The chain is: page → /Outer Do → /Inner Do → text.
func TestXObjectNestedRecursion(t *testing.T) {
	const want = "XObjText"
	innerBody := fmt.Sprintf("BT (%s) Tj ET", want)
	outerBody := "/Inner Do"
	pageContent := "/Outer Do"

	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page — only knows about /Outer
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Outer 4 0 R >> >> /Contents 5 0 R >>",
		// 4: Outer Form XObject — references /Inner via its own Resources
		formXObjStream(outerBody, "<< /XObject << /Inner 6 0 R >> >>"),
		// 5: Page content stream
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		// 6: Inner Form XObject — contains the actual text
		formXObjStream(innerBody, ""),
	})

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(got, want) {
		t.Errorf("GetPlainText = %q; want it to contain %q", got, want)
	}
}

// TestXObjectOwnFontResources verifies that when a Form XObject declares its
// own /Resources/Font dict, the XObject's fonts are used for decoding text
// inside it — not the parent page's fonts.
//
// Setup: page /F1 → WinAnsiEncoding (byte 0x80 → '€');
//
//	XObject /F1 → MacRomanEncoding (byte 0x80 → 'Ä').
//
// GetPlainText must decode the page byte as '€' and the XObject byte as 'Ä'.
func TestXObjectOwnFontResources(t *testing.T) {
	// PDF octal escape \200 == byte 0x80.
	pageContent := "BT /F1 12 Tf (\\200) Tj ET /Xi0 Do"
	xobjBody := "BT /F1 12 Tf (\\200) Tj ET"

	data := buildPDFFromObjects([]string{
		// 1: Catalog
		"<< /Type /Catalog /Pages 2 0 R >>",
		// 2: Pages
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// 3: Page — page /F1 = WinAnsiEncoding obj 6; XObject = obj 5
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> /XObject << /Xi0 5 0 R >> >> /Contents 4 0 R >>",
		// 4: Page content
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		// 5: Form XObject with its own /F1 → MacRomanEncoding (obj 7)
		formXObjStream(xobjBody, "<< /Font << /F1 7 0 R >> >>"),
		// 6: Page /F1 → WinAnsiEncoding (byte 0x80 → '€')
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		// 7: XObject /F1 → MacRomanEncoding (byte 0x80 → 'Ä')
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding >>",
	})

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	got, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	// Page byte 0x80 under WinAnsiEncoding → '€'
	if !strings.Contains(got, "€") {
		t.Errorf("GetPlainText = %q; want page byte 0x80 decoded as € (WinAnsi)", got)
	}
	// XObject byte 0x80 under MacRomanEncoding → 'Ä'
	if !strings.Contains(got, "Ä") {
		t.Errorf("GetPlainText = %q; want XObject byte 0x80 decoded as Ä (MacRoman)", got)
	}
}

// buildDeepXObjectPDF constructs a PDF with a chain of Form XObjects nested
// n levels deep.  The chain is:
//
//	page content → /X0 Do
//	  X0 → /X1 Do
//	  X1 → /X2 Do
//	  ...
//	  X(n-1) → /X(n) Do
//	  X(n) → "BT (marker) Tj ET"   ← deepest level
//
// Each XObject's resources reference the next via /XObject << /X(i+1) R >>.
// Objects are laid out so that obj 1=Catalog, 2=Pages, 3=Page, 4=page-content,
// 5..5+n-1=outer XObjects (X0..X(n-1)), 5+n=inner-most XObject (X(n)).
func buildDeepXObjectPDF(n int, marker string) []byte {
	// Object index 1..4 are fixed (Catalog, Pages, Page, page content).
	// Xobj objects start at index 5; there are n+1 of them (X0..Xn).
	//   obj 5      = X0
	//   obj 5+k    = Xk
	//   obj 5+n    = Xn  (innermost, contains marker text)
	totalObjs := 4 + n + 1 // Catalog + Pages + Page + content + (n+1) xobjs

	// Build the content of each XObject object (as raw PDF dict+stream strings).
	xobjObjs := make([]string, n+1)
	for k := 0; k <= n; k++ {
		objNum := 5 + k // 1-based object number for XObject k
		if k == n {
			// Innermost: contains the marker text
			body := fmt.Sprintf("BT (%s) Tj ET", marker)
			xobjObjs[k] = formXObjStream(body, "<< >>")
		} else {
			// Intermediate: calls next XObject
			nextObjNum := objNum + 1
			childName := fmt.Sprintf("X%d", k+1)
			body := fmt.Sprintf("/%s Do", childName)
			res := fmt.Sprintf("<< /XObject << /%s %d 0 R >> >>", childName, nextObjNum)
			xobjObjs[k] = formXObjStream(body, res)
		}
	}

	// Build the full objects slice (0-indexed for buildPDFFromObjects).
	objs := make([]string, totalObjs)
	// obj 1: Catalog
	objs[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	// obj 2: Pages
	objs[1] = "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"
	// obj 3: Page — references X0 (obj 5)
	objs[2] = "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /X0 5 0 R >> >> /Contents 4 0 R >>"
	// obj 4: page content — calls /X0 Do
	pageContent := "/X0 Do"
	objs[3] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent)
	// obj 5..5+n: XObjects
	for k, s := range xobjObjs {
		objs[4+k] = s
	}

	return buildPDFFromObjects(objs)
}

// TestXObjectDepthLimitGraceful verifies the behavior when Form XObjects are
// nested beyond xobjMaxDepth (currently 10).
//
// The depth guard in all three extraction paths (content.go, plaintext.go,
// walk.go) silently skips the Do operator once depth >= xobjMaxDepth.  This
// means text at nesting depth > xobjMaxDepth is silently absent from the
// output — no error is returned and no panic occurs.  This is intentional
// (see gstate.go comment: "guards against cyclic or deeply nested XObjects").
//
// Assertions:
//   - GetPlainText returns without panic and within 2 seconds (no hang)
//   - The returned error is nil (depth cap is not an error condition)
//   - The deep marker text is absent from the output (silent truncation)
func TestXObjectDepthLimitGraceful(t *testing.T) {
	const nestDepth = xobjMaxDepth + 2 // 12, two levels past the cap
	const marker = "DeepMarker"

	data := buildDeepXObjectPDF(nestDepth, marker)

	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := OpenBytes(data)
		if err != nil {
			ch <- result{err: err}
			return
		}
		got, err := r.Page(1).GetPlainText(nil)
		ch <- result{text: got, err: err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			// OpenBytes parse error means the fixture is broken, not the guard.
			t.Fatalf("unexpected error: %v", res.err)
		}
		// The depth guard does NOT return an error — it silently skips.
		// Confirm marker is absent (silently truncated beyond xobjMaxDepth).
		if strings.Contains(res.text, marker) {
			t.Errorf("GetPlainText = %q; depth cap should have silently truncated %q at depth %d",
				res.text, marker, nestDepth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GetPlainText hung on deeply nested XObject chain (potential infinite loop)")
	}
}

// TestXObjectDepthLimitContentText verifies the same depth-cap behavior for
// the Content().Text path (contentState.handleDo in content.go).
func TestXObjectDepthLimitContentText(t *testing.T) {
	const nestDepth = xobjMaxDepth + 2 // two levels past the cap
	const marker = "DeepMarkerContent"

	data := buildDeepXObjectPDF(nestDepth, marker)

	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := OpenBytes(data)
		if err != nil {
			ch <- result{err: err}
			return
		}
		texts := r.Page(1).Content().Text
		var sb strings.Builder
		for _, tx := range texts {
			sb.WriteString(tx.S)
		}
		ch <- result{text: sb.String()}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if strings.Contains(res.text, marker) {
			t.Errorf("Content().Text joined = %q; depth cap should have silently truncated %q at depth %d",
				res.text, marker, nestDepth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Content().Text hung on deeply nested XObject chain (potential infinite loop)")
	}
}
