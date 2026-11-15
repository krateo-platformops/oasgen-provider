package restdefinition

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// props is a tiny builder for a spec property tree.
func props(names ...string) map[string]apiextensionsv1.JSONSchemaProps {
	m := map[string]apiextensionsv1.JSONSchemaProps{}
	for _, n := range names {
		m[n] = apiextensionsv1.JSONSchemaProps{Type: "string"}
	}
	return m
}

func nested(parent string, children ...string) map[string]apiextensionsv1.JSONSchemaProps {
	return map[string]apiextensionsv1.JSONSchemaProps{
		parent: {Type: "object", Properties: props(children...)},
	}
}

func crdWith(versions ...apiextensionsv1.CustomResourceDefinitionVersion) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{Versions: versions},
	}
}

func version(name string, served bool, specProps map[string]apiextensionsv1.JSONSchemaProps) apiextensionsv1.CustomResourceDefinitionVersion {
	return apiextensionsv1.CustomResourceDefinitionVersion{
		Name:   name,
		Served: served,
		Schema: &apiextensionsv1.CustomResourceValidation{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"spec": {Type: "object", Properties: specProps},
				},
			},
		},
	}
}

func TestExcludedFieldsStillServed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		excluded []string
		crd      *apiextensionsv1.CustomResourceDefinition
		want     []string
	}{
		{
			name:     "a served top-level field is reported",
			excluded: []string{"id"},
			crd:      crdWith(version("v1alpha1", true, props("id", "name"))),
			want:     []string{"id"},
		},
		{
			name:     "a field the CRD does not serve is not reported",
			excluded: []string{"id"},
			crd:      crdWith(version("v1alpha1", true, props("name"))),
			want:     nil,
		},
		{
			name:     "a served NESTED path is reported",
			excluded: []string{"searchCriteria.title"},
			crd:      crdWith(version("v1alpha1", true, nested("searchCriteria", "title"))),
			want:     []string{"searchCriteria.title"},
		},
		{
			// THE CASE THAT SEPARATES A SEGMENT WALK FROM A PATH JOIN. The bracket form is ONE segment
			// whose name contains a dot. The CRD here serves the NESTED pair instead, so the exclusion
			// names a field that does not exist and must NOT be reported as pruning a served one.
			// Joining segments back with "." would make these two indistinguishable and report it.
			name:     "a bracket-escaped single segment is not matched by a nested pair of the same text",
			excluded: []string{`["searchCriteria.title"]`},
			crd:      crdWith(version("v1alpha1", true, nested("searchCriteria", "title"))),
			want:     nil,
		},
		{
			// The mirror, so the pair pins the distinction in both directions rather than only one.
			name:     "a nested path is not matched by a single field whose name contains the dot",
			excluded: []string{"searchCriteria.title"},
			crd:      crdWith(version("v1alpha1", true, props("searchCriteria.title"))),
			want:     nil,
		},
		{
			name:     "a field served by a NON-served version is not reported",
			excluded: []string{"legacy"},
			crd:      crdWith(version("v1alpha1", false, props("legacy"))),
			want:     nil,
		},
		{
			name:     "the union across served versions is used",
			excluded: []string{"onlyInOld"},
			crd: crdWith(
				version("v1alpha1", true, props("onlyInOld")),
				version("v1alpha2", true, props("current")),
			),
			want: []string{"onlyInOld"},
		},
		{
			name:     "no live CRD reports nothing",
			excluded: []string{"id"},
			crd:      nil,
			want:     nil,
		},
		{
			name:     "no exclusions reports nothing",
			excluded: nil,
			crd:      crdWith(version("v1alpha1", true, props("id"))),
			want:     nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := excludedFieldsStillServed(tc.excluded, servedSpecProperties(tc.crd))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestServesPathDoesNotMatchAnIntermediate pins that naming a parent does not count as serving the
// parent itself only -- the walk must reach the LAST segment, not stop early and answer yes.
func TestServesPathDoesNotMatchAnIntermediate(t *testing.T) {
	tree := nested("a", "b")
	if !servesPath(tree, []string{"a"}) {
		t.Error("a parent that exists should be served")
	}
	if !servesPath(tree, []string{"a", "b"}) {
		t.Error("a.b exists and should be served")
	}
	if servesPath(tree, []string{"a", "b", "c"}) {
		t.Error("a.b.c does not exist; walking past the leaf must not answer yes")
	}
	if servesPath(tree, []string{"b"}) {
		t.Error("b is not a top-level property")
	}
	if servesPath(tree, nil) {
		t.Error("an empty path cannot be served")
	}
}

// TestServedSpecPropertiesSkipsIncompleteSchemas is the setup control: each of these CRDs would
// panic or false-positive if the accessor chain were not guarded, so a pass here means the guards
// are doing something rather than that the inputs were benign.
func TestServedSpecPropertiesSkipsIncompleteSchemas(t *testing.T) {
	cases := map[string]*apiextensionsv1.CustomResourceDefinition{
		"no versions": crdWith(),
		"nil schema":  crdWith(apiextensionsv1.CustomResourceDefinitionVersion{Name: "v1", Served: true}),
		"nil openAPI": crdWith(apiextensionsv1.CustomResourceDefinitionVersion{Name: "v1", Served: true, Schema: &apiextensionsv1.CustomResourceValidation{}}),
	}
	for name, crd := range cases {
		t.Run(name, func(t *testing.T) {
			got := servedSpecProperties(crd)
			if len(got) != 0 {
				t.Fatalf("expected no served trees, got %d", len(got))
			}
			if out := excludedFieldsStillServed([]string{"id"}, got); out != nil {
				t.Fatalf("expected no warnings, got %v", out)
			}
		})
	}
	// And the positive control: a COMPLETE schema must produce one tree, or every row above passes
	// because the function always returns nothing.
	if got := servedSpecProperties(crdWith(version("v1", true, props("id")))); len(got) != 1 {
		t.Fatalf("POSITIVE CONTROL FAILED: a complete schema must yield 1 tree, got %d", len(got))
	}
}

// TestASpecWithNoPropertiesIsServedButEmpty is NOT a guard case, and my first version of the test
// above wrongly filed it as one. A `spec` that exists with no properties is a COMPLETE schema that
// serves nothing: the tree is returned (len 1) and the walk finds nothing in it. Asserting 0 trees
// here would have been asserting the wrong contract -- the thing that matters is that no warning
// comes out, not how many empty trees were collected on the way.
func TestASpecWithNoPropertiesIsServedButEmpty(t *testing.T) {
	served := servedSpecProperties(crdWith(version("v1", true, nil)))
	if len(served) != 1 {
		t.Fatalf("a spec that exists should yield its tree even when empty, got %d", len(served))
	}
	if out := excludedFieldsStillServed([]string{"id"}, served); out != nil {
		t.Fatalf("an empty served tree cannot be pruning anything, got %v", out)
	}
}
