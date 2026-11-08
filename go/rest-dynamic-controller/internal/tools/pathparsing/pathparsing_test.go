package pathparsing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePath(t *testing.T) {
	testCases := []struct {
		name          string
		path          string
		expected      []string
		expectError   bool
		errorContains string
	}{
		// Basic dot notation
		{
			name:     "Simple dot notation",
			path:     "a.b.c",
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "Single field",
			path:     "pullRequestId",
			expected: []string{"pullRequestId"},
		},
		{
			// WAS: []string{""}, nil. A plausible-but-wrong value -- it passed every `len(segs) == 0`
			// guard, so the caller believed it had a path and walked to a property named "". Now an
			// error (#513). Callers that can receive an unset optional field test path == "" first;
			// 12 of the 26 call sites already did.
			name:          "Empty path is an error, not one empty segment",
			path:          "",
			expectError:   true,
			errorContains: "empty",
		},

		// Bracket notation
		{
			name:     "Field with literal dot in brackets",
			path:     "['searchCriteria.creatorId']",
			expected: []string{"searchCriteria.creatorId"},
		},
		{
			name:     "Double quotes in brackets",
			path:     `["field.with.quotes"]`,
			expected: []string{"field.with.quotes"},
		},
		{
			name:     "Triple dots escaped",
			path:     "['a...b']",
			expected: []string{"a...b"},
		},

		// Mixed notation
		{
			name:     "Mixed notation",
			path:     "completionOptions.['option.name'].value",
			expected: []string{"completionOptions", "option.name", "value"},
		},
		{
			name:     "Literal dot field at end",
			path:     "some.nested.['field.with.dot']",
			expected: []string{"some", "nested", "field.with.dot"},
		},
		{
			name:     "Useless bracket",
			path:     "a.['b'].c",
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "Literal dot field in middle",
			path:     "user.['address.city'].street",
			expected: []string{"user", "address.city", "street"},
		},

		// Leading dots (attach to next segment)
		{
			// WAS: []string{".a", "b"} -- the dot was KEPT in the segment name, so ".a" addressed a
			// property literally called ".a". No document has one, so it resolved to nothing,
			// silently. Stripping it is a FIX, not compatibility: paths of this shape exist in the
			// wild because two of our own doc examples disagreed about the dialect.
			name:     "Leading dot is stripped",
			path:     ".a.b",
			expected: []string{"a", "b"},
		},
		{
			// WAS: []string{".a"}. Same change. This is the shape a consumer actually wrote --
			// `operationRef.path: .id` -- which addressed ".id" and found nothing.
			name:     "Single segment with leading dot is stripped",
			path:     ".a",
			expected: []string{"a"},
		},
		{
			// WAS: []string{"..a", "b"}. Same change, applied to all leading separators.
			name:     "Multiple leading dots are all stripped",
			path:     "..a.b",
			expected: []string{"a", "b"},
		},

		// Trailing dots (error)
		{
			name:          "Trailing dot",
			path:          "a.b.",
			expectError:   true,
			errorContains: "trailing separator",
		},
		{
			name:          "Single segment trailing dot",
			path:          "a.",
			expectError:   true,
			errorContains: "trailing separator",
		},

		// Consecutive dots (error)
		{
			name:          "Consecutive dots in middle",
			path:          "a..b",
			expectError:   true,
			errorContains: "empty segment",
		},

		// Dot + bracket combinations
		{
			// WAS: an error, "dot before bracket". Now valid, as a CONSEQUENCE of leading-dot
			// stripping rather than as a separate decision: the dot is removed, leaving ['a'].
			// This row is why the change is worth stating -- it was not in my first list of five.
			name:     "Dot before bracket is valid once the leading dot is stripped",
			path:     ".['a']",
			expected: []string{"a"},
		},
		{
			name:          "Dot after bracket at end",
			path:          "['a'].",
			expectError:   true,
			errorContains: "trailing separator",
		},
		{
			// WAS: an error, "adjacent brackets must be separated by dot". A bracket group is
			// self-delimiting, so requiring a dot between two of them was a restriction with no
			// ambiguity behind it.
			name:     "Adjacent brackets are separate segments",
			path:     "a.['b']['c']",
			expected: []string{"a", "b", "c"},
		},

		// Invalid bracket syntax
		{
			name:          "Unclosed bracket",
			path:          "['a.b.c",
			expectError:   true,
			errorContains: "unterminated bracket",
		},
		{
			name:          "Missing quotes in bracket",
			path:          "a.[b].c",
			expectError:   true,
			errorContains: "bracket must contain",
		},
		{
			// WAS: an error, "empty bracket content". Quoting is how a user says "this is the key I
			// mean", so an explicitly-quoted empty key is a key. An UNQUOTED a[] is still refused --
			// see the row below.
			name:     "An explicitly quoted empty key is a key",
			path:     "['']",
			expected: []string{""},
		},
		{
			name:          "An unquoted empty bracket is still malformed",
			path:          "a[]",
			expectError:   true,
			errorContains: "empty bracket",
		},

		// Spaces
		{
			name:          "Spaces in path",
			path:          "a. b.c",
			expectError:   true,
			errorContains: "unquoted whitespace",
		},
		{
			name:          "Spaces around brackets",
			path:          "[ 'field' ]",
			expectError:   true,
			errorContains: "bracket must contain",
		},

		// Special characters in segments
		{
			name:     "Numeric field names",
			path:     "user.['address.123'].street",
			expected: []string{"user", "address.123", "street"},
		},
		{
			name:     "Underscores and numbers",
			path:     "user.['address_123'].street",
			expected: []string{"user", "address_123", "street"},
		},
		{
			name:     "Underscores and dashes",
			path:     "user.['address_123-456'].street",
			expected: []string{"user", "address_123-456", "street"},
		},

		// Escaped leading/trailing dots in brackets
		{
			name:     "Leading dot escaped in bracket",
			path:     "['.leading'].dot",
			expected: []string{".leading", "dot"},
		},
		{
			name:     "Trailing dot escaped in bracket",
			path:     "trailing.['dot.']",
			expected: []string{"trailing", "dot."},
		},

		// Only dots (error)
		{
			name:          "Only dots",
			path:          "...",
			expectError:   true,
			errorContains: "nothing but separators",
		},

		// Double quotes in brackets
		{
			name:     "Double quotes in brackets mixed",
			path:     "user.[\"address.city\"].street",
			expected: []string{"user", "address.city", "street"},
		},

		// Array index bracket notation (issue #33)
		{
			name:     "Array index in brackets",
			path:     "credentials[0].value",
			expected: []string{"credentials", "0", "value"},
		},
		{
			name:     "Multi-digit array index",
			path:     "items[12].name",
			expected: []string{"items", "12", "name"},
		},
		{
			name:     "Array index at path start",
			path:     "[0].value",
			expected: []string{"0", "value"},
		},
		{
			name:     "Array index at path end",
			path:     "credentials[0]",
			expected: []string{"credentials", "0"},
		},
		{
			// WAS: an error, "adjacent brackets". Same change as the row above; renamed because the
			// old name asserted a limitation that no longer holds.
			name:     "Chained array indices are supported",
			path:     "matrix[0][1]",
			expected: []string{"matrix", "0", "1"},
		},
		{
			name:     "Dotted numeric segment equivalent to bracket index",
			path:     "credentials.0.value",
			expected: []string{"credentials", "0", "value"},
		},
		{
			name:          "Redundant leading zero index is rejected as an index but still invalid unquoted",
			path:          "credentials[00].value",
			expectError:   true,
			errorContains: "bracket must contain",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			segments, err := ParsePath(tc.path)

			if tc.expectError {
				require.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, segments)
			}
		})
	}
}
