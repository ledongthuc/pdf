# PDF Reader

A simple Go library which enables reading PDF files. Forked from https://github.com/ledongthuc/pdf

Features
  - Get plain text content (without format)
  - Get Content (including all font and formatting information)

## Install:

`go get -u github.com/gage-marshall/pdf`

## Examples:

 - Check in examples/ folder


## Read plain text

```golang
package main

import (
	"bytes"
	"fmt"

	"github.com/gage-marshall/pdf"
)

func main() {
	pdf.DebugOn = true

	f, r, err := pdf.Open("./pdf_test.pdf")
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
	content := buf.String()
	fmt.Println(content)
}
```

## Read all text with styles from PDF

```golang
package main

import (
	"fmt"

	"github.com/gage-marshall/pdf"
)

func main() {
	f, r, err := pdf.Open("./pdf_test.pdf")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	sentences, err := r.GetStyledTexts()
	if err != nil {
		panic(err)
	}

	// Print all sentences
	for _, sentence := range sentences {
		fmt.Printf("Font: %s, Font-size: %f, x: %f, y: %f, content: %s \n",
			sentence.Font,
			sentence.FontSize,
			sentence.X,
			sentence.Y,
			sentence.S)
	}
}
```


## Read text grouped by rows

```golang
package main

import (
	"fmt"
	"os"

	"github.com/gage-marshall/pdf"
)

func main() {
	content, err := readPdf(os.Args[1]) // Read local pdf file
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
	return
}

func readPdf(path string) (string, error) {
	f, r, err := pdf.Open(path)
	defer func() {
		_ = f.Close()
	}()
	if err != nil {
		return "", err
	}
	totalPage := r.NumPage()

	for pageIndex := 1; pageIndex <= totalPage; pageIndex++ {
		p := r.Page(pageIndex)
		if p.V.IsNull() || p.V.Key("Contents").Kind() == pdf.Null {
			continue
		}

		rows, _ := p.GetTextByRow()
		for _, row := range rows {
		    println(">>>> row: ", row.Position)
		    for _, word := range row.Content {
		        fmt.Println(word.S)
		    }
		}
	}
	return "", nil
}
```

## Demo
![Run example](https://i.gyazo.com/01fbc539e9872593e0ff6bac7e954e6d.gif)
