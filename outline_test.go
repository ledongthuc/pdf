package pdf

import (
	"fmt"
	"strings"
	"testing"
)

// buildOutlinePDF constructs a minimal valid PDF with numPages real pages
// and an optional /Outlines tree. destPageNum (1-based) is the page the
// first outline entry links to via /Dest; pass 0 to omit /Dest.
// actionPageNum is the page linked via /A /GoTo; pass 0 to omit.
// If title is empty, no /Outlines entry is included in the catalog.
//
// Object layout:
//
//	1: /Catalog (Root) — references /Pages 2 0 R and optionally /Outlines N 0 R
//	2: /Pages node — /Kids [3 0 R … (2+numPages) 0 R] /Count numPages
//	3 … 2+numPages: individual /Page objects
//	2+numPages+1: /Outlines root (if title != "")
//	2+numPages+2: first outline item with /Dest (if destPageNum > 0)
//	2+numPages+3: second outline item with /A /GoTo (if actionPageNum > 0)
func buildOutlinePDF(numPages int, title string, destPageNum int, actionPageNum int) []byte {
	// Build the Kids list for the Pages node (objects 3 … 2+numPages).
	var kidRefs []string
	for i := 1; i <= numPages; i++ {
		kidRefs = append(kidRefs, fmt.Sprintf("%d 0 R", 2+i))
	}
	kidsStr := strings.Join(kidRefs, " ")

	// Collect all object bodies in order.
	// objs[i] is the body for object number i+1.
	var objs []string

	// We'll patch object 1 (Catalog) after we know the outline object number.
	// Reserve slot 0 for Catalog (filled in later).
	objs = append(objs, "") // placeholder for Catalog

	// Object 2: Pages
	objs = append(objs, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kidsStr, numPages))

	// Objects 3 … 2+numPages: individual Page objects
	for i := 1; i <= numPages; i++ {
		objs = append(objs, "<< /Type /Page /Parent 2 0 R >>")
	}

	// Now add Outlines objects if title is non-empty.
	outlineRootObjNum := 0
	if title != "" {
		outlineRootObjNum = len(objs) + 1 // next object number

		// Collect child outline item object numbers.
		var firstChildObjNum int
		var childObjNums []int

		// Dest item
		if destPageNum > 0 {
			childObjNums = append(childObjNums, outlineRootObjNum+1+len(childObjNums))
		}
		// Action item
		if actionPageNum > 0 {
			childObjNums = append(childObjNums, outlineRootObjNum+1+len(childObjNums))
		}

		if len(childObjNums) > 0 {
			firstChildObjNum = childObjNums[0]
		}

		// Build Outlines root dict
		if firstChildObjNum > 0 {
			lastChildObjNum := childObjNums[len(childObjNums)-1]
			objs = append(objs, fmt.Sprintf("<< /Type /Outlines /First %d 0 R /Last %d 0 R /Count %d >>",
				firstChildObjNum, lastChildObjNum, len(childObjNums)))
		} else {
			objs = append(objs, "<< /Type /Outlines /Count 0 >>")
		}

		// Build child item objects.
		// For /Next linking we need to know consecutive item numbers.
		destItemObjNum := 0
		actionItemObjNum := 0
		idx := 0
		if destPageNum > 0 {
			destItemObjNum = childObjNums[idx]
			idx++
		}
		if actionPageNum > 0 {
			actionItemObjNum = childObjNums[idx]
		}

		// Dest outline item
		if destPageNum > 0 {
			pageObjNum := 2 + destPageNum // page N is object 2+N
			nextStr := ""
			if actionItemObjNum > 0 {
				nextStr = fmt.Sprintf(" /Next %d 0 R", actionItemObjNum)
			}
			objs = append(objs, fmt.Sprintf(
				"<< /Title (%s) /Parent %d 0 R /Dest [%d 0 R /XYZ null null null]%s >>",
				title, outlineRootObjNum, pageObjNum, nextStr))
		}

		// Action outline item
		if actionPageNum > 0 {
			pageObjNum := 2 + actionPageNum
			prevStr := ""
			if destItemObjNum > 0 {
				prevStr = fmt.Sprintf(" /Prev %d 0 R", destItemObjNum)
			}
			objs = append(objs, fmt.Sprintf(
				"<< /Title (%s via action) /Parent %d 0 R /A << /S /GoTo /D [%d 0 R /Fit] >>%s >>",
				title, outlineRootObjNum, pageObjNum, prevStr))
		}
	}

	// Now fill in the Catalog (object 1).
	if outlineRootObjNum > 0 {
		objs[0] = fmt.Sprintf("<< /Type /Catalog /Pages 2 0 R /Outlines %d 0 R >>", outlineRootObjNum)
	} else {
		objs[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	}

	// Serialise the PDF.
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")

	// Track byte offsets for xref (1-indexed: off[i] is offset for object i).
	off := make([]int, len(objs)+1)
	for i, body := range objs {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj %s endobj\n", i+1, body)
	}

	xrefOff := b.Len()
	n := len(objs) + 1 // total objects including free entry 0
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", n)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}

	trailerRoot := fmt.Sprintf("/Root 1 0 R")
	fmt.Fprintf(&b, "trailer\n<< /Size %d %s >>\nstartxref\n%d\n%%%%EOF\n", n, trailerRoot, xrefOff)

	return []byte(b.String())
}

// buildPDFWithNamedDest constructs a 1-page PDF whose single outline item
// has /Dest as a Name (not an array). Named dest resolution is deferred;
// the correct result is Page == 0.
func buildPDFWithNamedDest() []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R /Outlines 4 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R >>",
		"<< /Type /Outlines /First 5 0 R /Last 5 0 R /Count 1 >>",
		"<< /Title (Named) /Parent 4 0 R /Dest /SomeName >>",
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
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", n, xrefOff)
	return []byte(b.String())
}

func TestOutlineNamedDestReturnsZero(t *testing.T) {
	data := buildPDFWithNamedDest()
	r, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes failed: %v", err)
	}
	if got := r.NumPage(); got != 1 {
		t.Fatalf("NumPage() = %d, want 1", got)
	}
	outline := r.Outline()
	if len(outline.Child) == 0 {
		t.Fatal("expected at least one outline child")
	}
	if got := outline.Child[0].Page; got != 0 {
		t.Errorf("Child[0].Page = %d, want 0 for named dest", got)
	}
}

func TestOutlinePageNumber(t *testing.T) {
	// ----------------------------------------------------------------
	// Case 1: explicit /Dest array → correct page number
	// ----------------------------------------------------------------
	t.Run("DestArray", func(t *testing.T) {
		data := buildOutlinePDF(3, "Chapter", 2, 0)
		r, err := OpenBytes(data)
		if err != nil {
			t.Fatalf("OpenBytes failed: %v", err)
		}
		// Guard: fixture must have the expected page count.
		if got := r.NumPage(); got != 3 {
			t.Fatalf("NumPage() = %d, want 3", got)
		}
		outline := r.Outline()
		if len(outline.Child) != 1 {
			t.Fatalf("Outline.Child count = %d, want 1", len(outline.Child))
		}
		if got := outline.Child[0].Page; got != 2 {
			t.Errorf("Child[0].Page = %d, want 2", got)
		}
		if got := outline.Child[0].Title; got != "Chapter" {
			t.Errorf("Child[0].Title = %q, want %q", got, "Chapter")
		}
	})

	// ----------------------------------------------------------------
	// Case 2: /A /S /GoTo → correct page number
	// ----------------------------------------------------------------
	t.Run("ActionGoTo", func(t *testing.T) {
		data := buildOutlinePDF(4, "Section", 0, 3)
		r, err := OpenBytes(data)
		if err != nil {
			t.Fatalf("OpenBytes failed: %v", err)
		}
		if got := r.NumPage(); got != 4 {
			t.Fatalf("NumPage() = %d, want 4", got)
		}
		outline := r.Outline()
		if len(outline.Child) != 1 {
			t.Fatalf("Outline.Child count = %d, want 1", len(outline.Child))
		}
		if got := outline.Child[0].Page; got != 3 {
			t.Errorf("Child[0].Page = %d, want 3", got)
		}
	})

	// ----------------------------------------------------------------
	// Case 3: outline item with no dest → Page == 0
	// ----------------------------------------------------------------
	t.Run("NoDest", func(t *testing.T) {
		data := buildOutlinePDF(2, "NoLink", 0, 0)
		r, err := OpenBytes(data)
		if err != nil {
			t.Fatalf("OpenBytes failed: %v", err)
		}
		if got := r.NumPage(); got != 2 {
			t.Fatalf("NumPage() = %d, want 2", got)
		}
		outline := r.Outline()
		// When title is "NoLink" but destPageNum==0 and actionPageNum==0, the
		// Outlines root has no children (no child item objects were written).
		// The root itself has Page==0.
		if got := outline.Page; got != 0 {
			t.Errorf("root outline.Page = %d, want 0", got)
		}
		if len(outline.Child) != 0 {
			t.Errorf("Outline.Child count = %d, want 0", len(outline.Child))
		}
	})

	// ----------------------------------------------------------------
	// Case 4: PDF with no /Outlines → empty outline, Page == 0
	// ----------------------------------------------------------------
	t.Run("NoOutlines", func(t *testing.T) {
		data := buildOutlinePDF(1, "", 0, 0)
		r, err := OpenBytes(data)
		if err != nil {
			t.Fatalf("OpenBytes failed: %v", err)
		}
		if got := r.NumPage(); got != 1 {
			t.Fatalf("NumPage() = %d, want 1", got)
		}
		outline := r.Outline()
		if outline.Page != 0 {
			t.Errorf("outline.Page = %d, want 0", outline.Page)
		}
		if outline.Title != "" {
			t.Errorf("outline.Title = %q, want empty", outline.Title)
		}
		if len(outline.Child) != 0 {
			t.Errorf("outline.Child count = %d, want 0", len(outline.Child))
		}
	})

	// ----------------------------------------------------------------
	// Case 5: both Dest and Action items in the same PDF
	// ----------------------------------------------------------------
	t.Run("BothDestAndAction", func(t *testing.T) {
		data := buildOutlinePDF(5, "Mixed", 1, 4)
		r, err := OpenBytes(data)
		if err != nil {
			t.Fatalf("OpenBytes failed: %v", err)
		}
		if got := r.NumPage(); got != 5 {
			t.Fatalf("NumPage() = %d, want 5", got)
		}
		outline := r.Outline()
		if len(outline.Child) != 2 {
			t.Fatalf("Outline.Child count = %d, want 2", len(outline.Child))
		}
		if got := outline.Child[0].Page; got != 1 {
			t.Errorf("Child[0].Page = %d, want 1", got)
		}
		if got := outline.Child[1].Page; got != 4 {
			t.Errorf("Child[1].Page = %d, want 4", got)
		}
	})
}
