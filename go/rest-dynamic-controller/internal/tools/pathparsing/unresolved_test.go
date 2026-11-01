package pathparsing

import (
	"testing"
)

// Moved here with the implementation it covers (#361). It used to live in the builder package's test file,
// which is how a helper four callers relied on ended up tested in a package the URL-building code cannot
// import.
func TestUnresolvedPathParams(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		params map[string]string
		want   []string
	}{
		{
			name:   "all populated",
			path:   "/admin/realms/{realm}/users/{id}",
			params: map[string]string{"realm": "demo", "id": "abc"},
		},
		{
			// The motivating case: create never succeeded, so status.id was never written.
			name:   "identifier never written to status",
			path:   "/admin/realms/{realm}/users/{id}",
			params: map[string]string{"realm": "demo"},
			want:   []string{"id"},
		},
		{
			// An empty value is NOT a substitution: it would address /users/ instead of /users/{id}.
			name:   "present but empty counts as unresolved",
			path:   "/admin/realms/{realm}/users/{id}",
			params: map[string]string{"realm": "demo", "id": ""},
			want:   []string{"id"},
		},
		{
			name:   "reports every missing placeholder in order",
			path:   "/admin/realms/{realm}/users/{id}",
			params: map[string]string{},
			want:   []string{"realm", "id"},
		},
		{
			// A parameterless delete is addressable without any identifier — it must NOT be skipped.
			name:   "no placeholders",
			path:   "/admin/session",
			params: map[string]string{},
		},
		{
			name:   "unterminated placeholder does not panic",
			path:   "/users/{id",
			params: map[string]string{},
		},
		{
			name:   "empty placeholder name is ignored",
			path:   "/users/{}",
			params: map[string]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := UnresolvedPathParams(tc.path, tc.params)
			if len(got) != len(tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %#v, want %#v", got, tc.want)
				}
			}
		})
	}
}
