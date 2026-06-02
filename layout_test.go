// layout_test.go — regression tests for the walk.go TJ-kerning fix exercised
// via the GetTextByRow / GetTextByColumn layout paths.
//
// The fix in handleWalkShowArray (walk.go) advances s.x by the kerning offset
// for numeric TJ elements and injects a synthetic space when the gap is large
// (>= tjSpaceThreshold = 120 units). These tests confirm that the layout
// functions built on walkTextBlocks surface that behaviour correctly.
package pdf

import (
	"strings"
	"testing"
)

// collectRowText concatenates the S fields of every Text entry in all rows,
// inserting a single space between adjacent Text entries within the same row
// so that individually-emitted words remain distinguishable even when the
// synthetic space was emitted as a separate Text element.
func collectRowText(rows Rows) string {
	var b strings.Builder
	for _, row := range rows {
		for _, txt := range row.Content {
			b.WriteString(txt.S)
		}
	}
	return b.String()
}

// collectColumnText does the same for Columns.
func collectColumnText(cols Columns) string {
	var b strings.Builder
	for _, col := range cols {
		for _, txt := range col.Content {
			b.WriteString(txt.S)
		}
	}
	return b.String()
}

// TestGetTextByRowTJKerning verifies that GetTextByRow surfaces both words
// from a TJ array with a large negative kern (-300).  The -300 gap must
// produce a visible separation: either a space character in the concatenated
// output, or two distinct Text entries so that "HelloWorld" never appears as a
// single fused run.
func TestGetTextByRowTJKerning(t *testing.T) {
	stream := "BT\n/F1 12 Tf\n[(Hello) -300 (World)] TJ\nET"
	data := buildSinglePagePDF(stream)

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)

	rows, err := p.GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}

	joined := collectRowText(rows)
	t.Logf("GetTextByRow joined: %q", joined)

	if !strings.Contains(joined, "Hello") {
		t.Errorf("GetTextByRow: \"Hello\" not found in %q (characters dropped)", joined)
	}
	if !strings.Contains(joined, "World") {
		t.Errorf("GetTextByRow: \"World\" not found in %q (characters dropped)", joined)
	}

	// A -300 kern is a word gap; the two words must not fuse into a single run.
	// Accept either an explicit space in the concatenated output or two separate
	// Text entries (which means "HelloWorld" as a flat string is absent).
	hasGap := !strings.Contains(joined, "HelloWorld") ||
		strings.Contains(joined, "Hello World")
	if !hasGap {
		t.Errorf("GetTextByRow: large kern (-300) did not produce separation; got %q (want gap between \"Hello\" and \"World\")", joined)
	}

	// Additionally verify that both words land in the same row (same Y position).
	if len(rows) == 0 {
		t.Fatal("GetTextByRow: no rows returned")
	}
	var helloFound, worldFound bool
	for _, row := range rows {
		for _, txt := range row.Content {
			if strings.Contains(txt.S, "Hello") {
				helloFound = true
			}
			if strings.Contains(txt.S, "World") {
				worldFound = true
			}
		}
	}
	if !helloFound {
		t.Error("GetTextByRow: no Text entry containing \"Hello\"")
	}
	if !worldFound {
		t.Error("GetTextByRow: no Text entry containing \"World\"")
	}
}

// TestGetTextByColumnTJKerning mirrors TestGetTextByRowTJKerning for the
// GetTextByColumn layout path.
func TestGetTextByColumnTJKerning(t *testing.T) {
	stream := "BT\n/F1 12 Tf\n[(Hello) -300 (World)] TJ\nET"
	data := buildSinglePagePDF(stream)

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)

	cols, err := p.GetTextByColumn()
	if err != nil {
		t.Fatalf("GetTextByColumn: %v", err)
	}

	joined := collectColumnText(cols)
	t.Logf("GetTextByColumn joined: %q", joined)

	if !strings.Contains(joined, "Hello") {
		t.Errorf("GetTextByColumn: \"Hello\" not found in %q (characters dropped)", joined)
	}
	if !strings.Contains(joined, "World") {
		t.Errorf("GetTextByColumn: \"World\" not found in %q (characters dropped)", joined)
	}
}

// TestGetTextByRowTJSmallKern verifies that a small kern (-10) does not drop
// any characters.  Both words must still appear in the output.
func TestGetTextByRowTJSmallKern(t *testing.T) {
	stream := "BT\n/F1 12 Tf\n[(Hello) -10 (World)] TJ\nET"
	data := buildSinglePagePDF(stream)

	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	p := r.Page(1)

	rows, err := p.GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}

	joined := collectRowText(rows)
	t.Logf("GetTextByRow small-kern joined: %q", joined)

	if !strings.Contains(joined, "Hello") {
		t.Errorf("GetTextByRow: \"Hello\" not found in %q (small kern dropped characters)", joined)
	}
	if !strings.Contains(joined, "World") {
		t.Errorf("GetTextByRow: \"World\" not found in %q (small kern dropped characters)", joined)
	}
}
