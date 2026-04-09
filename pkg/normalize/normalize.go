package normalize

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var whitespaceRe = regexp.MustCompile(`\s+`)

// Normalize applies a series of deterministic transformations to reduce
// surface variation between semantically identical prompts.
func Normalize(s string) string {
	// 1. Unicode NFC normalization
	s = norm.NFC.String(s)

	// 2. Lowercase
	s = strings.ToLower(s)

	// 3. Collapse internal whitespace
	s = whitespaceRe.ReplaceAllString(s, " ")

	// 4. Trim
	s = strings.TrimSpace(s)

	// 5. Strip trailing sentence-ending punctuation and any surrounding spaces,
	//    repeating until stable (handles cases like "hello !" or "end . ! ;").
	for {
		t := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ".?!;"))
		if t == s {
			break
		}
		s = t
	}

	return s
}
