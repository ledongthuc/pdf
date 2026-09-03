package render

import (
	"bytes"
	"io"
	"strconv"
	"unicode"
)

// operator represents a PDF content stream operator with its operands.
type operator struct {
	name     string
	operands []any
}

// parser tokenizes and parses PDF content streams.
type parser struct {
	data []byte
	pos  int
}

// newParser creates a new content stream parser.
func newParser(data []byte) *parser {
	return &parser{data: data}
}

// nextOperator returns the next operator from the content stream.
func (p *parser) nextOperator() (*operator, error) {
	var operands []any

	for {
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			return nil, io.EOF
		}

		// Check what kind of token we have
		ch := p.data[p.pos]

		switch {
		case ch == '%':
			// Comment - skip to end of line
			p.skipComment()

		case ch == '(':
			// Literal string
			s, err := p.readLiteralString()
			if err != nil {
				return nil, err
			}
			operands = append(operands, s)

		case ch == '<':
			// Hex string or dictionary
			if p.pos+1 < len(p.data) && p.data[p.pos+1] == '<' {
				// Dictionary - skip for now (used in inline images)
				p.skipDictionary()
			} else {
				// Hex string
				s, err := p.readHexString()
				if err != nil {
					return nil, err
				}
				operands = append(operands, s)
			}

		case ch == '/':
			// Name
			name := p.readName()
			operands = append(operands, name)

		case ch == '[':
			// Array
			arr, err := p.readArray()
			if err != nil {
				return nil, err
			}
			operands = append(operands, arr)

		case ch == ']':
			// End of array (shouldn't happen at top level)
			p.pos++

		case isDigit(ch) || ch == '-' || ch == '+' || ch == '.':
			// Number
			num := p.readNumber()
			operands = append(operands, num)

		case isAlpha(ch):
			// Operator or keyword
			word := p.readWord()

			// Check for special keywords
			switch word {
			case "true":
				operands = append(operands, true)
			case "false":
				operands = append(operands, false)
			case "null":
				operands = append(operands, nil)
			case "BI":
				// Begin inline image - read until EI
				imgData := p.readInlineImage()
				return &operator{name: "BI", operands: []any{imgData}}, nil
			default:
				// Regular operator
				return &operator{name: word, operands: operands}, nil
			}

		default:
			// Unknown character - skip
			p.pos++
		}
	}
}

// skipWhitespace skips whitespace and null bytes.
func (p *parser) skipWhitespace() {
	for p.pos < len(p.data) {
		ch := p.data[p.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == '\x00' {
			p.pos++
		} else {
			break
		}
	}
}

// skipComment skips a comment to end of line.
func (p *parser) skipComment() {
	for p.pos < len(p.data) && p.data[p.pos] != '\n' && p.data[p.pos] != '\r' {
		p.pos++
	}
}

// readLiteralString reads a parenthesized string.
func (p *parser) readLiteralString() (string, error) {
	if p.pos >= len(p.data) || p.data[p.pos] != '(' {
		return "", io.ErrUnexpectedEOF
	}
	p.pos++ // skip '('

	var buf bytes.Buffer
	depth := 1

	for p.pos < len(p.data) && depth > 0 {
		ch := p.data[p.pos]
		p.pos++

		switch ch {
		case '(':
			depth++
			buf.WriteByte(ch)
		case ')':
			depth--
			if depth > 0 {
				buf.WriteByte(ch)
			}
		case '\\':
			// Escape sequence
			if p.pos < len(p.data) {
				escaped := p.data[p.pos]
				p.pos++
				switch escaped {
				case 'n':
					buf.WriteByte('\n')
				case 'r':
					buf.WriteByte('\r')
				case 't':
					buf.WriteByte('\t')
				case 'b':
					buf.WriteByte('\b')
				case 'f':
					buf.WriteByte('\f')
				case '(':
					buf.WriteByte('(')
				case ')':
					buf.WriteByte(')')
				case '\\':
					buf.WriteByte('\\')
				case '\r':
					// Line continuation
					if p.pos < len(p.data) && p.data[p.pos] == '\n' {
						p.pos++
					}
				case '\n':
					// Line continuation
				default:
					// Octal escape
					if escaped >= '0' && escaped <= '7' {
						octal := int(escaped - '0')
						for i := 0; i < 2 && p.pos < len(p.data); i++ {
							if p.data[p.pos] >= '0' && p.data[p.pos] <= '7' {
								octal = octal*8 + int(p.data[p.pos]-'0')
								p.pos++
							} else {
								break
							}
						}
						buf.WriteByte(byte(octal))
					} else {
						buf.WriteByte(escaped)
					}
				}
			}
		default:
			buf.WriteByte(ch)
		}
	}

	return buf.String(), nil
}

// readHexString reads a hexadecimal string.
func (p *parser) readHexString() (string, error) {
	if p.pos >= len(p.data) || p.data[p.pos] != '<' {
		return "", io.ErrUnexpectedEOF
	}
	p.pos++ // skip '<'

	var hexBuf bytes.Buffer
	for p.pos < len(p.data) {
		ch := p.data[p.pos]
		if ch == '>' {
			p.pos++
			break
		}
		if isHexDigit(ch) {
			hexBuf.WriteByte(ch)
		}
		p.pos++
	}

	// Decode hex string
	hex := hexBuf.String()
	if len(hex)%2 == 1 {
		hex += "0" // Pad with zero
	}

	var result bytes.Buffer
	for i := 0; i+1 < len(hex); i += 2 {
		b, _ := strconv.ParseUint(hex[i:i+2], 16, 8)
		result.WriteByte(byte(b))
	}

	return result.String(), nil
}

// readName reads a PDF name object.
func (p *parser) readName() string {
	if p.pos >= len(p.data) || p.data[p.pos] != '/' {
		return ""
	}
	p.pos++ // skip '/'

	var buf bytes.Buffer
	for p.pos < len(p.data) {
		ch := p.data[p.pos]
		if isDelimiter(ch) || isWhitespace(ch) {
			break
		}
		if ch == '#' && p.pos+2 < len(p.data) {
			// Hex escape
			hex := string(p.data[p.pos+1 : p.pos+3])
			b, err := strconv.ParseUint(hex, 16, 8)
			if err == nil {
				buf.WriteByte(byte(b))
				p.pos += 3
				continue
			}
		}
		buf.WriteByte(ch)
		p.pos++
	}

	return buf.String()
}

// readArray reads a PDF array.
func (p *parser) readArray() ([]any, error) {
	if p.pos >= len(p.data) || p.data[p.pos] != '[' {
		return nil, io.ErrUnexpectedEOF
	}
	p.pos++ // skip '['

	var arr []any

	for {
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			return nil, io.ErrUnexpectedEOF
		}

		if p.data[p.pos] == ']' {
			p.pos++
			break
		}

		ch := p.data[p.pos]

		switch {
		case ch == '(':
			s, err := p.readLiteralString()
			if err != nil {
				return nil, err
			}
			arr = append(arr, s)

		case ch == '<':
			if p.pos+1 < len(p.data) && p.data[p.pos+1] == '<' {
				p.skipDictionary()
			} else {
				s, err := p.readHexString()
				if err != nil {
					return nil, err
				}
				arr = append(arr, s)
			}

		case ch == '/':
			name := p.readName()
			arr = append(arr, name)

		case ch == '[':
			subArr, err := p.readArray()
			if err != nil {
				return nil, err
			}
			arr = append(arr, subArr)

		case isDigit(ch) || ch == '-' || ch == '+' || ch == '.':
			num := p.readNumber()
			arr = append(arr, num)

		case isAlpha(ch):
			word := p.readWord()
			switch word {
			case "true":
				arr = append(arr, true)
			case "false":
				arr = append(arr, false)
			case "null":
				arr = append(arr, nil)
			default:
				arr = append(arr, word)
			}

		default:
			p.pos++
		}
	}

	return arr, nil
}

// readNumber reads a number (integer or real).
func (p *parser) readNumber() float64 {
	var buf bytes.Buffer

	// Handle sign
	if p.pos < len(p.data) && (p.data[p.pos] == '-' || p.data[p.pos] == '+') {
		buf.WriteByte(p.data[p.pos])
		p.pos++
	}

	// Read digits and decimal point
	hasDecimal := false
	for p.pos < len(p.data) {
		ch := p.data[p.pos]
		if isDigit(ch) {
			buf.WriteByte(ch)
			p.pos++
		} else if ch == '.' && !hasDecimal {
			buf.WriteByte(ch)
			hasDecimal = true
			p.pos++
		} else {
			break
		}
	}

	val, _ := strconv.ParseFloat(buf.String(), 64)
	return val
}

// readWord reads an alphabetic word (operator or keyword).
func (p *parser) readWord() string {
	var buf bytes.Buffer

	for p.pos < len(p.data) {
		ch := p.data[p.pos]
		if isAlpha(ch) || ch == '*' || ch == '\'' || ch == '"' {
			buf.WriteByte(ch)
			p.pos++
		} else {
			break
		}
	}

	return buf.String()
}

// skipDictionary skips a dictionary (<<...>>).
func (p *parser) skipDictionary() {
	if p.pos+1 >= len(p.data) || p.data[p.pos] != '<' || p.data[p.pos+1] != '<' {
		return
	}
	p.pos += 2 // skip '<<'

	depth := 1
	for p.pos < len(p.data) && depth > 0 {
		if p.pos+1 < len(p.data) {
			if p.data[p.pos] == '<' && p.data[p.pos+1] == '<' {
				depth++
				p.pos += 2
				continue
			}
			if p.data[p.pos] == '>' && p.data[p.pos+1] == '>' {
				depth--
				p.pos += 2
				continue
			}
		}
		p.pos++
	}
}

// readInlineImage reads an inline image's data.
func (p *parser) readInlineImage() []byte {
	// Skip to ID (image data)
	for p.pos < len(p.data) {
		if p.pos+1 < len(p.data) && p.data[p.pos] == 'I' && p.data[p.pos+1] == 'D' {
			p.pos += 2
			// Skip whitespace after ID
			if p.pos < len(p.data) && isWhitespace(p.data[p.pos]) {
				p.pos++
			}
			break
		}
		p.pos++
	}

	// Read image data until EI
	start := p.pos
	for p.pos < len(p.data) {
		// Look for EI preceded by whitespace
		if p.pos+2 < len(p.data) &&
			isWhitespace(p.data[p.pos]) &&
			p.data[p.pos+1] == 'E' &&
			p.data[p.pos+2] == 'I' {
			end := p.pos
			p.pos += 3
			return p.data[start:end]
		}
		p.pos++
	}

	return p.data[start:]
}

// Helper functions

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func isAlpha(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isHexDigit(ch byte) bool {
	return isDigit(ch) || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}

func isWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == '\x00' || ch == '\x0c'
}

func isDelimiter(ch byte) bool {
	return ch == '(' || ch == ')' || ch == '<' || ch == '>' ||
		ch == '[' || ch == ']' || ch == '{' || ch == '}' ||
		ch == '/' || ch == '%'
}

func isRegular(ch byte) bool {
	return !isWhitespace(ch) && !isDelimiter(ch)
}

func isUnicodeWhitespace(r rune) bool {
	return unicode.IsSpace(r)
}
