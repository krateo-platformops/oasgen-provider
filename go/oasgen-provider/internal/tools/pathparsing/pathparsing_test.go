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
			// WAS: []string{""}, nil -- a plausible-but-wrong value that passed every len(segs)==0 guard
			// and then named a property called "". Now an error (#513).
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
			// WAS: []string{".a", "b"} -- the dot was KEPT, so ".a" named a property literally called
			// ".a". No schema has one, so the lookup failed and was reported as the field being absent.
			name:     "Leading dot is stripped",
			path:     ".a.b",
			expected: []string{"a", "b"},
		},
		{
			// WAS: []string{".a"}. Same change. This is the shape a consumer wrote as
			// `operationRef.path: .id`, on the strength of a doc example in the other dialect.
			name:     "Single segment with leading dot is stripped",
			path:     ".a",
			expected: []string{"a"},
		},
		{
			// WAS: []string{"..a", "b"}. Same change, for every leading separator.
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
			// WAS: an error, "dot before bracket". Now valid as a CONSEQUENCE of leading-dot stripping.
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
			// WAS: an error, "adjacent brackets". A bracket group is self-delimiting, so requiring a dot
			// between two of them was a restriction with no ambiguity behind it.
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
			// WAS: an error, "empty bracket content". Quoting is how an author says "this is the name I
			// mean", so an explicitly-quoted empty key is a key. An UNQUOTED a[] is still refused.
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
		// THE REFUSAL THIS STEP ADDS. The old parser rejected these too, as "invalid characters in
		// segment" -- an accident of its grammar rather than a decision. They are now refused with a
		// message that says WHY, because every caller in this module names the properties of a SCHEMA,
		// and a schema describes one element shape rather than indexed elements.
		{
			name:          "An array index is refused, naming the reason",
			path:          "credentials[0].value",
			expectError:   true,
			errorContains: "cannot address anything here",
		},
		{
			name:          "A bare numeric segment is refused as an index",
			path:          "credentials[12]",
			expectError:   true,
			errorContains: "an array index",
		},
		{
			name:          "A predicate is refused, naming the reason",
			path:          "credentials[?name=admin].value",
			expectError:   true,
			errorContains: "a [?key=value] selector",
		},
		{
			// The wording here tracks the message, and this row is why: the first draft of the message
			// interpolated s.String() and suggested ['[0]'] -- a key literally named "[0]". Fixing that
			// changed the wording, and THIS ROW still asserted the old phrasing. It passed locally
			// because the suite had been run before the fix and only a probe after it.
			name:          "The refusal tells the author how to mean it literally",
			path:          "credentials[0]",
			expectError:   true,
			errorContains: `If "0" is a literal property name, quote it: ['0']`,
		},
		{
			// A QUOTED numeric segment is a KEY, not an index, and must still work -- this is the
			// escape the refusal above points at, so it has to be exercised or the message is advice
			// that does not work.
			name:     "A quoted numeric segment is a key",
			path:     "credentials.['0'].value",
			expected: []string{"credentials", "0", "value"},
		},
		{
			name:     "Double quotes in brackets mixed",
			path:     "user.[\"address.city\"].street",
			expected: []string{"user", "address.city", "street"},
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
