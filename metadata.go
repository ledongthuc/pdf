package pdf

import (
	"strconv"
	"time"
)

// Info wraps the /Info dictionary of a PDF document.
type Info struct {
	V Value
}

// Info returns the document's information dictionary.
func (r *Reader) Info() Info {
	return Info{r.Trailer().Key("Info")}
}

func (i Info) Title() string    { return i.V.Key("Title").Text() }
func (i Info) Author() string   { return i.V.Key("Author").Text() }
func (i Info) Subject() string  { return i.V.Key("Subject").Text() }
func (i Info) Keywords() string { return i.V.Key("Keywords").Text() }
func (i Info) Creator() string  { return i.V.Key("Creator").Text() }
func (i Info) Producer() string { return i.V.Key("Producer").Text() }
func (i Info) Trapped() string  { return i.V.Key("Trapped").Name() }

func (i Info) CreationDate() time.Time { return parsePDFDate(i.V.Key("CreationDate").RawString()) }
func (i Info) ModDate() time.Time      { return parsePDFDate(i.V.Key("ModDate").RawString()) }

// consumeInt reads the first 2 characters of s as a decimal integer.
// Returns (parsed value, s[2:]) on success, or (def, s) if s is too short or non-numeric.
func consumeInt(s string, def int) (int, string) {
	if len(s) < 2 {
		return def, s
	}
	v, err := strconv.Atoi(s[:2])
	if err != nil {
		return def, s
	}
	return v, s[2:]
}

// parseTZOffset parses the timezone suffix of a PDF date string (§14.3.3).
// Recognised forms: "Z", "+HH'mm'", "-HH'mm'".
// Returns time.UTC for any unrecognised or zero-offset input.
func parseTZOffset(s string) *time.Location {
	if len(s) < 1 {
		return time.UTC
	}
	switch s[0] {
	case 'Z':
		return time.UTC
	case '+', '-':
		sign := 1
		if s[0] == '-' {
			sign = -1
		}
		s = s[1:]
		var tzHour int
		tzHour, s = consumeInt(s, 0)
		if len(s) >= 1 && s[0] == '\'' {
			s = s[1:]
		}
		tzMin, _ := consumeInt(s, 0)
		offset := sign * (tzHour*3600 + tzMin*60)
		if offset == 0 {
			return time.UTC
		}
		return time.FixedZone("", offset)
	}
	return time.UTC
}

// parsePDFDate parses a PDF date string per spec §14.3.3.
// Format: D:YYYYMMDDHHmmSSOHH'mm' where O is +/-/Z and all fields after YYYY are optional.
// Returns zero time on empty input or parse failure.
func parsePDFDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if len(s) >= 2 && s[:2] == "D:" {
		s = s[2:]
	}
	if len(s) < 4 {
		return time.Time{}
	}
	year, err := strconv.Atoi(s[:4])
	if err != nil {
		return time.Time{}
	}
	s = s[4:]

	var month, day, hour, min, sec int
	month, s = consumeInt(s, 1)
	day, s = consumeInt(s, 1)
	hour, s = consumeInt(s, 0)
	min, s = consumeInt(s, 0)
	sec, s = consumeInt(s, 0)

	return time.Date(year, time.Month(month), day, hour, min, sec, 0, parseTZOffset(s))
}
