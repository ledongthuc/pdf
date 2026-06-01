# PDF Reader

A Go library for reading PDF files, with active CJK text extraction support.

Forked from [ledongthuc/pdf](https://github.com/ledongthuc/pdf) (upstream inactive since 2024).
Original lineage: [rsc/pdf](https://github.com/rsc/pdf).

## Features

- Plain text extraction (no formatting)
- Styled text extraction (font name, size, position)
- Text grouped by row
- **CJK predefined CMap decoders** (added in this fork):
  - Japanese Shift-JIS (`90ms-RKSJ-H/V`, `90pv-RKSJ-H`)
  - CJK UCS-2 BE (`UniGB-UCS2-H/V`, `UniCNS-UCS2-H/V`, `UniJIS-UCS2-H/V`, `UniKS-UCS2-H/V`)
  - Simplified Chinese GBK (`GBK-EUC-H/V`)
  - Traditional Chinese Big5-ETen (`ETen-B5-H/V`)
  - Korean UHC/EUC-KR (`KSCms-UHC-H/V`)

## Install

```bash
go get github.com/Detective-XH/pdf
```

To use this fork in a project that already depends on `ledongthuc/pdf`, add a
`replace` directive:

```
replace github.com/ledongthuc/pdf => github.com/Detective-XH/pdf v0.0.0-latest
```

## Examples

See the `examples/` folder for runnable programs.

### Read plain text

```go
package main

import (
	"bytes"
	"fmt"

	"github.com/ledongthuc/pdf"
)

func main() {
	f, r, err := pdf.Open("./sample.pdf")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	var buf bytes.Buffer
	b, err := r.GetPlainText()
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
	"fmt"

	"github.com/ledongthuc/pdf"
)

func main() {
	f, r, err := pdf.Open("./sample.pdf")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	sentences, err := r.GetStyledTexts()
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

	"github.com/ledongthuc/pdf"
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
| GBK CMaps | Added |
| Big5-ETen CMaps | Added |
| UHC/EUC-KR CMaps | Added |
| Upstream PRs incorporated | #37, #42, #45 |
