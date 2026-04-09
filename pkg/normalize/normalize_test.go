package normalize

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "LowercaseConversion",
			input:    "HOW DO I Reset",
			expected: "how do i reset",
		},
		{
			name:     "WhitespaceCollapse",
			input:    "hello   world\t\nfoo",
			expected: "hello world foo",
		},
		{
			name:     "TrailingPunctuation",
			input:    "reset my password?",
			expected: "reset my password",
		},
		{
			name:     "MultiplePunctuation",
			input:    "help!!!",
			expected: "help",
		},
		{
			name:     "InternalPunctuation",
			input:    "it's a semi-colon; test",
			expected: "it's a semi-colon; test",
		},
		{
			name:     "UnicodeNFC",
			input:    "e\u0301", // decomposed é (e + combining acute accent)
			expected: "\u00e9",  // composed é
		},
		{
			name:     "CJKCharacters",
			input:    "\u4f60\u597d\u4e16\u754c",
			expected: "\u4f60\u597d\u4e16\u754c",
		},
		{
			name:     "EmptyString",
			input:    "",
			expected: "",
		},
		{
			name:     "OnlyPunctuation",
			input:    "???",
			expected: "",
		},
		{
			name:     "OnlyWhitespace",
			input:    "   \t  ",
			expected: "",
		},
		{
			name:     "LeadingTrailingSpaces",
			input:    "  hello  ",
			expected: "hello",
		},
		{
			name:     "TrailingSpaceBeforePunctuation",
			input:    "hello !",
			expected: "hello",
		},
		{
			name:     "SpaceBeforePunctuation2",
			input:    "what is this ?",
			expected: "what is this",
		},
		{
			name:     "TrailingSpacesAfterPunctuation",
			input:    "test .  ",
			expected: "test",
		},
		{
			name:     "MultipleSpacesAndPunctuation",
			input:    "end . ! ;",
			expected: "end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestNormalize_Idempotent(t *testing.T) {
	inputs := []string{
		"HOW DO I Reset",
		"hello   world\t\nfoo",
		"reset my password?",
		"help!!!",
		"it's a semi-colon; test",
		"e\u0301",
		"\u4f60\u597d\u4e16\u754c",
		"",
		"???",
		"   \t  ",
		"  hello  ",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			once := Normalize(input)
			twice := Normalize(once)
			assert.Equal(t, once, twice, "Normalize should be idempotent for input: %q", input)
		})
	}
}
