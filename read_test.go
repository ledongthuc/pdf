package pdf

import (
	"testing"
)

func TestRead(t *testing.T) {
	t.Run("HeaderValidation", testHeaderValidation)
}

func testHeaderValidation(t *testing.T) {
	tscs := map[string]struct {
		input         []byte
		expectedValid bool
	}{
		"nil": {
			input:         nil,
			expectedValid: false,
		},
		"empty": {
			input:         []byte{},
			expectedValid: false,
		},
		"missing LF": {
			input:         []byte{37, 80, 68, 70, 45, 49, 46, 55},
			expectedValid: false,
		},
		"ok LF": {
			input:         []byte{37, 80, 68, 70, 45, 49, 46, 55, 10},
			expectedValid: true,
		},
		"invalid version 1.8": {
			input:         []byte{37, 80, 68, 70, 45, 49, 46, 58, 10},
			expectedValid: false,
		},
		"ok CRLF": {
			input:         []byte{37, 80, 68, 70, 45, 49, 46, 55, 13, 10},
			expectedValid: true,
		},
		"ok space + CRLF": {
			input:         []byte{37, 80, 68, 70, 45, 49, 46, 55, 32, 13, 10},
			expectedValid: true,
		},
	}
	for name, data := range tscs {
		data := data
		t.Run(name, func(t *testing.T) {
			gotValid := headerRegexp.Match(data.input)
			if gotValid != data.expectedValid {
				t.Errorf("expected %t, got %t", data.expectedValid, gotValid)
			}
		})
	}
}
