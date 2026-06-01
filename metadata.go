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

	month := 1
	if len(s) >= 2 {
		if v, err := strconv.Atoi(s[:2]); err == nil {
			month = v
			s = s[2:]
		}
	}
	day := 1
	if len(s) >= 2 {
		if v, err := strconv.Atoi(s[:2]); err == nil {
			day = v
			s = s[2:]
		}
	}
	hour := 0
	if len(s) >= 2 {
		if v, err := strconv.Atoi(s[:2]); err == nil {
			hour = v
			s = s[2:]
		}
	}
	min := 0
	if len(s) >= 2 {
		if v, err := strconv.Atoi(s[:2]); err == nil {
			min = v
			s = s[2:]
		}
	}
	sec := 0
	if len(s) >= 2 {
		if v, err := strconv.Atoi(s[:2]); err == nil {
			sec = v
			s = s[2:]
		}
	}

	loc := time.UTC
	if len(s) >= 1 {
		switch s[0] {
		case 'Z':
			loc = time.UTC
		case '+', '-':
			sign := 1
			if s[0] == '-' {
				sign = -1
			}
			s = s[1:]
			tzHour := 0
			if len(s) >= 2 {
				if v, err := strconv.Atoi(s[:2]); err == nil {
					tzHour = v
					s = s[2:]
				}
			}
			if len(s) >= 1 && s[0] == '\'' {
				s = s[1:]
			}
			tzMin := 0
			if len(s) >= 2 {
				if v, err := strconv.Atoi(s[:2]); err == nil {
					tzMin = v
				}
			}
			offset := sign * (tzHour*3600 + tzMin*60)
			if offset == 0 {
				loc = time.UTC
			} else {
				loc = time.FixedZone("", offset)
			}
		}
	}

	return time.Date(year, time.Month(month), day, hour, min, sec, 0, loc)
}
