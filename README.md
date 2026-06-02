# PDF Reader

[![Go CI](https://github.com/Detective-XH/pdf/actions/workflows/ci.yml/badge.svg)](https://github.com/Detective-XH/pdf/actions/workflows/ci.yml)
[![License: BSD 3-Clause](https://img.shields.io/badge/License-BSD_3--Clause-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/Detective-XH/pdf.svg)](https://pkg.go.dev/github.com/Detective-XH/pdf)
[![Go Report Card](https://goreportcard.com/badge/github.com/Detective-XH/pdf)](https://goreportcard.com/report/github.com/Detective-XH/pdf)

A Go library for reading PDF files, with active CJK text extraction support.

**Requires Go 1.25+** (`go.mod` directive).

Forked from [ledongthuc/pdf](https://github.com/ledongthuc/pdf) (upstream inactive since 2024).
Original lineage: [rsc/pdf](https://github.com/rsc/pdf).

## Features

- Plain text extraction with context/cancellation support
- Styled text extraction (font name, size, position)
- Text grouped by row
- Document metadata API (`/Info` dict: title, author, dates, …)
- Outline (table of contents) with resolved page numbers
- **CJK predefined CMap decoders**:
  - Japanese Shift-JIS (`90ms-RKSJ-H/V`, `90pv-RKSJ-H`)
  - CJK UCS-2 BE (`UniGB-UCS2-H/V`, `UniCNS-UCS2-H/V`, `UniJIS-UCS2-H/V`, `UniKS-UCS2-H/V`)
  - Simplified Chinese GBK / GB-EUC / GBKp-EUC (`GBK-EUC-H/V`, `GB-EUC-H/V`, `GBKp-EUC-H/V`)
  - Traditional Chinese Big5-ETen / ETenms (`ETen-B5-H/V`, `ETenms-B5-H/V`)
  - Korean UHC / KSC-EUC / UHC-HW (`KSCms-UHC-H/V`, `KSC-EUC-H/V`, `KSCms-UHC-HW-H/V`)

## Install

```bash
go get github.com/Detective-XH/pdf
```

## Examples

See the `examples/` folder for runnable programs.

### Read plain text

```go
package main

import (
	"bytes"
	"context"
	"fmt"

	"github.com/Detective-XH/pdf"
)

func main() {
	f, r, err := pdf.Open("./sample.pdf")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	var buf bytes.Buffer
	b, err := r.GetPlainText(context.Background())
	if err != nil {
		panic(err)
	}
	buf.ReadFrom(b)
	fmt.Println(buf.String())
}
```

### Read styled text

```go
package main

import (
	"context"
	"fmt"

	"github.com/Detective-XH/pdf"
)

func main() {
	f, r, err := pdf.Open("./sample.pdf")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	sentences, err := r.GetStyledTexts(context.Background())
	if err != nil {
		panic(err)
	}
	for _, s := range sentences {
		fmt.Printf("font=%s size=%.1f x=%.1f y=%.1f text=%s\n",
			s.Font, s.FontSize, s.X, s.Y, s.S)
	}
}
```

### Read text by row

```go
package main

import (
	"fmt"
	"os"

	"github.com/Detective-XH/pdf"
)

func main() {
	f, r, err := pdf.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()

	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		rows, _ := p.GetTextByRow()
		for _, row := range rows {
			fmt.Printf("row %d:", row.Position)
			for _, word := range row.Content {
				fmt.Printf(" %s", word.S)
			}
			fmt.Println()
		}
	}
}
```

## Fork status

| Area | Status |
|------|--------|
| Upstream sync | Merged through upstream@HEAD (2024) |
| Shift-JIS CMaps | Added |
| UCS-2 BE CMaps | Added |
| GBK / GB-EUC / GBKp-EUC CMaps | Added |
| Big5-ETen / ETenms CMaps | Added |
| UHC / KSC-EUC / UHC-HW CMaps | Added |
| Metadata API (`r.Info()`) | Added |
| Outline page numbers (`Outline.Page`) | Added |
| Context / cancellation | Added |
| Crash/CPU-spike on PDFs with inline images ([upstream #57](https://github.com/ledongthuc/pdf/issues/57)) | Fixed — `readHexString` EOF guard + `Interpret` inline-image skip |
| Upstream PRs incorporated | #37, #42, #45, #58, #61, #63, #64, #66 |

### Resolved upstream issues

| Issue | Title | How it was fixed | Status |
|-------|-------|------------------|--------|
| [#13](https://github.com/ledongthuc/pdf/issues/13) | Load Reader from bytes instead of file path | `OpenBytes(src []byte)` added in `read.go` | Directly fixed |
| [#16](https://github.com/ledongthuc/pdf/issues/16) | GetTextByRow returns disordered text | `sort.Sort` → `sort.Stable` in `GetTextByRow`/`GetTextByColumn` | Directly fixed |
| [#18](https://github.com/ledongthuc/pdf/issues/18) | GetTextByRow X/Y always 0 | `Td`/`TD`/`T*`/`TL` wired in `walkTextBlocks`; `BT` resets position; `currentTL` tracks leading | Directly fixed |
| [#20](https://github.com/ledongthuc/pdf/issues/20) | `%%EOF` search window too small; valid PDFs rejected | Expanded search window from 100 → 1024 bytes (with clamp for small files); added `findStartxrefFallback` reverse-scan for `%%EOF` placed further than 1024 bytes before end | Directly fixed |
| [#21](https://github.com/ledongthuc/pdf/issues/21) | unknown encoding UniGB-UCS2-H | Same fix as #55 — `ucs2BEEncoder` handles `UniGB-UCS2-H` | Directly fixed |
| [#22](https://github.com/ledongthuc/pdf/issues/22) | Handle space after header | Relaxed byte-8 check in `NewReaderEncrypted` to accept space/tab | Directly fixed |
| [#27](https://github.com/ledongthuc/pdf/issues/27) | GetTextByRow returns empty rows | `Td` in `walkTextBlocks` now updates `currentX`/`currentY` additively instead of emitting a spurious empty walker call; `TD` and `TL` wired; `T*` decrements Y by leading | Directly fixed |
| [#30](https://github.com/ledongthuc/pdf/issues/30) | crash when encountering some CJK text amongst English | `dictEncoder` rewrite; `maxObjectDepth` guard; `readArray` EOF fix | Directly fixed |
| [#31](https://github.com/ledongthuc/pdf/issues/31) | Expose page dimensions | `Page.MediaBox()` and `Page.CropBox()` added; both walk the page-tree inheritance chain; `CropBox` falls back to `MediaBox` when absent | Directly fixed |
| [#44](https://github.com/ledongthuc/pdf/issues/44) | Cannot read Chinese | GBK / Big5 / UniGB / UniCNS CMaps all wired in `getEncoder()` | Directly fixed |
| [#48](https://github.com/ledongthuc/pdf/issues/48) | `\n` added by recent version breaks old systems | Removed `showText("\n")` from `case "BT":` — BT is matrix-init, not line-break | Directly fixed |
| [#55](https://github.com/ledongthuc/pdf/issues/55) | GetPlainText do not support encoding "UniGB-UCS2-H" | `ucs2BEEncoder` wired for all 8 `Uni*-UCS2-H/V` CMap names | Directly fixed |
| [#57](https://github.com/ledongthuc/pdf/issues/57) | Crash when image is in there (malformed PNG) | `case "ID":` skip in `ps.go` `Interpret()`; `readHexString` EOF guard in `lex.go` | Directly fixed |
| [#59](https://github.com/ledongthuc/pdf/issues/59) | Streaming / range-over-func API for large PDFs | `(*Reader).Pages() iter.Seq2[int, Page]` and `(Page).Texts() iter.Seq[Text]` added; `Texts` merges same-style runs matching `GetStyledTexts` output | Directly fixed |
| [#60](https://github.com/ledongthuc/pdf/issues/60) | Parse PDF, some content appears garbled | Removed shared `fonts` map from `(*Reader).GetPlainText`; each page now passes `nil` so `(*Page).GetPlainText` builds a fresh per-page font map | Directly fixed |
| [#67](https://github.com/ledongthuc/pdf/issues/67) / [#26](https://github.com/ledongthuc/pdf/issues/26) | Text in Form XObjects not extracted | `case "Do":` added to all three interpreter callbacks (`interpretPlain`, `interpretWalk`, `interpret`); each handler looks up the named XObject in the current `/Resources/XObject` dict, confirms `Subtype /Form`, builds a child state with fonts re-resolved from the XObject's own `/Resources/Font`, and recursively calls `Interpret()` — merging extracted text back into the parent output; depth capped at 10 to guard against cycles | Directly fixed |
| [unipdf#524](https://github.com/unidoc/unipdf/issues/524) | TJ kerning array silently drops word-spacing (`Content` path) | `interpretTJArray` (`content.go`): numeric elements ≤ −120 thousandths emit a synthetic space before the next string segment; threshold 120 is a conservative word-gap value. | Fork fix — `Content()`/`GetStyledTexts()` path |
| [unipdf#524](https://github.com/unidoc/unipdf/issues/524) | TJ kerning array silently drops word-spacing (`GetTextByRow`/`GetTextByColumn` path) | `handleWalkShowArray` (`walk.go`): added `fontSize` field to `walkState`; numeric TJ elements now advance `s.x` and inject a synthetic space when gap ≥ 120 units. | Fork fix — walk path |
| fork-security | Malformed CMap input causes panic / DoS | (1) `handleEndBfchar`/`handleEndBfrange` used `panic(string)` that escaped `readCmap`/`Interpret` uncaught — replaced with `s.ok=false; return`. (2) `beginbfchar` count with no upper bound allowed 2-billion-iteration hang — capped at `maxCmapEntries=65536`. Confirmed by red-team probes P4a/P4b/P4c. | Fork-internal security hardening |
| fork-security | xref stream `Size` field unbounded before `make()` | `readXrefStream` allocated `make([]xref, size)` directly from the untrusted PDF `Size` field with no upper-bound check; a crafted value ≥ 1 GB would exhaust memory. Added `size <= 0 \|\| size > maxXrefObjects` guard (reusing the existing 8 388 607 constant) before the allocation. Confirmed by red-team probe P6. | Fork-internal security hardening |
