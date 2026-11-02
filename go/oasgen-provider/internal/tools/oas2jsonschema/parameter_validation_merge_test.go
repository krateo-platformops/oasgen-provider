package oas2jsonschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

// A parameter whose name collides with a body property used to lose its ENTIRE schema -- only `required`
// survived -- so a `pattern` or `maxLength` declared on the parameter was read, dropped at the merge, and
// enforced by nothing (#257).
//
// Measured on a live cluster at 0.38.0: a field that was both a create-body property and a path parameter
// kept the body's `pattern`, while one declared ONLY as a path parameter lost it entirely.
//
// Worse than not supporting the keyword, because the document reads as though the constraint applies.
func TestCollidingParameterKeywordsAreMergedWhenTheBodyIsSilent(t *testing.T) {
	existing := &Schema{Type: []string{"string"}}
	param := &Schema{
		Type:      []string{"string"},
		Pattern:   "^(heads|tags)/.+",
		MaxLength: i64(255),
		MinLength: i64(1),
	}

	warnings := reconcileParameterValidation(existing, param, "ref", "get")

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: the body declared no competing keyword, so there is nothing "+
			"ambiguous to report", warnings)
	}
	if existing.Pattern != "^(heads|tags)/.+" {
		t.Errorf("pattern = %q, want it merged. This is the keyword measured as lost on a live cluster", existing.Pattern)
	}
	if existing.MaxLength == nil || *existing.MaxLength != 255 {
		t.Error("maxLength was not merged")
	}
	if existing.MinLength == nil || *existing.MinLength != 1 {
		t.Error("minLength was not merged -- note 0 is a REAL constraint here, which is why these are pointers")
	}
}

// The ambiguous case gets a diagnostic, not a decision. Silently merging a conflict would be the same
// defect in the other direction: tightening a constraint the author never asked for, on a field whose body
// declaration they may have chosen deliberately.
func TestCollidingParameterKeywordsConflictIsWarnedNotResolved(t *testing.T) {
	existing := &Schema{Type: []string{"string"}, Pattern: "^refs/.+", MaxLength: i64(100)}
	param := &Schema{Type: []string{"string"}, Pattern: "^(heads|tags)/.+", MaxLength: i64(255)}

	warnings := reconcileParameterValidation(existing, param, "ref", "get")

	if len(warnings) != 2 {
		t.Fatalf("warnings = %d, want 2 (pattern and maxLength both differ): %v", len(warnings), warnings)
	}
	if existing.Pattern != "^refs/.+" {
		t.Errorf("pattern = %q, want the BODY's kept. Choosing the parameter's would silently change what "+
			"the CRD accepts, which is the author's call and not ours", existing.Pattern)
	}
	if existing.MaxLength == nil || *existing.MaxLength != 100 {
		t.Error("maxLength: want the body's kept")
	}
	for _, w := range warnings {
		if !strings.Contains(w.Error(), "ref") {
			t.Errorf("warning does not name the field: %v", w)
		}
		if !strings.Contains(w.Error(), "get") {
			t.Errorf("warning does not name the verb, so an author cannot find which declaration to fix: %v", w)
		}
	}
}

// Same value on both sides is not a conflict and must not warn -- otherwise a correct document that
// declares a constraint in both places produces noise, and noisy warnings get ignored.
func TestCollidingParameterKeywordsAgreeingIsSilent(t *testing.T) {
	existing := &Schema{Pattern: "^x.+", Maximum: f64(10)}
	param := &Schema{Pattern: "^x.+", Maximum: f64(10)}

	if w := reconcileParameterValidation(existing, param, "n", "get"); len(w) != 0 {
		t.Errorf("warnings = %v, want none: both declare the same constraint", w)
	}
}

// Bools have no conflict case: false is both the zero value and OAS's default, so only a true can be a
// declaration. Merging true over false loses nothing; the reverse would invent a declaration.
func TestCollidingParameterBoolKeywordsMergeOnlyUpward(t *testing.T) {
	existing := &Schema{}
	param := &Schema{ExclusiveMinimum: true, UniqueItems: true}
	if w := reconcileParameterValidation(existing, param, "n", "get"); len(w) != 0 {
		t.Errorf("unexpected warnings: %v", w)
	}
	if !existing.ExclusiveMinimum || !existing.UniqueItems {
		t.Error("a true on the parameter must reach the property")
	}

	// And the reverse direction must not clear an existing true.
	existing2 := &Schema{ExclusiveMaximum: true}
	param2 := &Schema{ExclusiveMaximum: false}
	reconcileParameterValidation(existing2, param2, "n", "get")
	if !existing2.ExclusiveMaximum {
		t.Error("the property's true was cleared by the parameter's default-false, inventing a relaxation")
	}
}

// Enum is deliberately NOT combined. An intersection can be empty and a union weakens both, so a
// difference is reported rather than resolved -- but an enum the body does not declare at all is merged,
// because nothing is being overridden.
func TestCollidingParameterEnumIsMergedOrWarnedNeverCombined(t *testing.T) {
	merged := &Schema{}
	if w := reconcileParameterValidation(merged, &Schema{Enum: []interface{}{"a", "b"}}, "n", "get"); len(w) != 0 {
		t.Errorf("unexpected warnings merging an enum into a silent property: %v", w)
	}
	if len(merged.Enum) != 2 {
		t.Error("an enum the body does not declare must be merged")
	}

	conflicting := &Schema{Enum: []interface{}{"a", "b"}}
	w := reconcileParameterValidation(conflicting, &Schema{Enum: []interface{}{"a", "c"}}, "n", "get")
	if len(w) != 1 {
		t.Fatalf("warnings = %d, want 1 for differing enums: %v", len(w), w)
	}
	if len(conflicting.Enum) != 2 || conflicting.Enum[1] != "b" {
		t.Error("the enums were combined; they must not be — an intersection may be empty and a union " +
			"weakens both, so this is the author's call")
	}
}

// THE PREMISE TEST. Every case above calls reconcileParameterValidation DIRECTLY, so all of them keep
// passing if addParametersToSpec never calls it -- which is precisely the defect: a function that exists
// and is not reached. That exact gap has been shipped in this repo twice.
//
// This drives the real generator against the shape the issue measured on a live cluster: `ref` is both a
// create-body property and a path parameter carrying the pattern, and `branch` is a path parameter only
// (the control -- it proves the non-colliding path still carries keywords, so the test cannot pass by
// losing everything).
const collidingParamSpec = `
openapi: 3.0.3
info: {title: t, version: "1.0"}
paths:
  /repos/refs:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                ref: {type: string}
                name: {type: string}
      responses:
        '201': {description: created}
  /repos/refs/{ref}:
    get:
      parameters:
        - name: ref
          in: path
          required: true
          schema: {type: string, pattern: "^(heads|tags)/.+", maxLength: 255}
        - name: branch
          in: query
          schema: {type: string, pattern: "^feature/.+"}
      responses:
        '200': {description: ok}
`

func TestGeneratorMergesCollidingParameterKeywordsEndToEnd(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(collidingParamSpec))
	if err != nil {
		t.Fatal(err)
	}

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "post", Path: "/repos/refs"},
			{Action: "get", Method: "get", Path: "/repos/refs/{ref}"},
		},
		Identifiers: []string{"name"},
	})

	specBytes, _, err := g.BuildSpecSchema()
	if err != nil {
		t.Fatal(err)
	}

	var schema map[string]any
	if err := jsonUnmarshal(specBytes, &schema); err != nil {
		t.Fatal(err)
	}
	props, _ := schema["properties"].(map[string]any)

	// The collision: `ref` is declared by the body (no keywords) AND by the path parameter (with them).
	ref, ok := props["ref"].(map[string]any)
	if !ok {
		t.Fatal("`ref` is missing from the generated spec entirely; the premise of this test is gone")
	}
	if ref["pattern"] != "^(heads|tags)/.+" {
		t.Errorf("ref.pattern = %v, want the parameter's. This is the exact keyword measured as LOST on a "+
			"live cluster at 0.38.0: the body declares `ref` without constraints, the path parameter "+
			"declares the pattern, and the merge used to discard the parameter's whole schema (#257)",
			ref["pattern"])
	}
	if ref["maxLength"] == nil {
		t.Error("ref.maxLength is absent: the parameter declares it and the body does not, so it must merge")
	}

	// The control: a parameter that does NOT collide must still carry its keywords, so a regression that
	// drops everything cannot pass this test.
	branch, ok := props["branch"].(map[string]any)
	if !ok {
		t.Fatal("`branch` is missing: the non-colliding parameter path is broken, which would make the " +
			"assertion above meaningless")
	}
	if branch["pattern"] != "^feature/.+" {
		t.Errorf("branch.pattern = %v, want ^feature/.+ -- the non-colliding path is the control", branch["pattern"])
	}
}
