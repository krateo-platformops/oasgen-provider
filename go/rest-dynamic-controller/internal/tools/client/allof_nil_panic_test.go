package restclient

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
)

// A panic in this package takes the controller pod down, and the trigger is an author's typo: a $ref in a
// request body that does not resolve. #302.
//
// Two independent halves, each asserted, because either alone would have prevented the crash and fixing
// only one leaves the other as a trap for the next caller:
//
//  1. populateFromAllOf must survive a nil schema;
//  2. the allOf loop must check BuildSchema's error BEFORE recursing, since on error it returns nil.
//
// The array branch of the same function already did (2) correctly ten lines above the allOf branch that
// did not -- the identical failure handled correctly in one place and incorrectly in the other, which is
// what makes this a bug rather than a design choice.
func TestPopulateFromAllOfSurvivesANilSchema(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on a nil schema: %v.\n\n"+
				"BuildSchema returns (nil, err) for an unresolvable $ref, so this input is reachable "+
				"from a real document -- and a panic here crashes the controller for every resource it "+
				"manages, not just the one with the bad ref", r)
		}
	}()
	populateFromAllOf(nil)
}

// And the whole parse path must tolerate a document whose allOf member cannot be resolved, which is the
// end-to-end shape rather than the unit one.
//
// Scope stated honestly: I could not drive libopenapi to hand populateFromAllOf a nil through a FULLY
// parsed document -- an unresolvable ref fails earlier, inside the library's own build. So this asserts
// that such a document is handled without a panic from our side, and the nil-safety above covers the case
// where it does get through. The route to nil is code-derived (err-after-recurse), not reproduced here.
func TestAnUnresolvableAllOfRefDoesNotPanicTheParsePath(t *testing.T) {
	const doc = `
openapi: 3.0.3
info: {title: t, version: "1.0"}
paths:
  /things:
    post:
      operationId: createThing
      requestBody:
        content:
          application/json:
            schema:
              type: object
              allOf:
                - $ref: '#/components/schemas/Missing'
      responses: {"201": {description: ok}}
components:
  schemas: {}
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic while parsing a document with an unresolvable allOf $ref: %v", r)
		}
	}()

	d, err := libopenapi.NewDocument([]byte(strings.TrimSpace(doc)))
	if err != nil {
		t.Skipf("document would not parse at all: %v", err)
	}
	model, errs := d.BuildV3Model()
	if model == nil {
		t.Skipf("no model built (%v); the library rejects this document before our code sees it, which "+
			"is a safe outcome but means this test cannot exercise the guard", errs)
	}

	// Walk every request body schema the way RequestedBody does, which is what calls populateFromAllOf.
	for path, item := range model.Model.Paths.PathItems.FromOldest() {
		for method, op := range item.GetOperations().FromOldest() {
			if op.RequestBody == nil {
				continue
			}
			for mt, media := range op.RequestBody.Content.FromOldest() {
				if media.Schema == nil {
					continue
				}
				sch, berr := media.Schema.BuildSchema()
				if berr != nil {
					t.Logf("%s %s (%s): BuildSchema errored as expected: %v", method, path, mt, berr)
					continue
				}
				populateFromAllOf(sch)
			}
		}
	}
}

// TestAllOfLoopChecksBuildErrorBeforeRecursing pins the SECOND half of the fix, which the nil guard
// otherwise masks.
//
// Mutation-testing the two halves separately showed this gap: restoring the original ordering --
// recursing into propSchema and only then checking err -- left every other test in this file passing,
// because the nil guard added above absorbs the nil. The ordering is still wrong (it processes a schema
// the library has just told us it could not build), and with the guard present nothing observable fails,
// so a future reader tidying this loop could reintroduce it freely.
//
// Asserting behaviour is impossible here without a document that drives BuildSchema to (nil, err) through
// a fully parsed model, and libopenapi rejects such a document earlier -- see the skip in the test above.
// So this asserts the source order instead, which is the property that actually matters.
func TestAllOfLoopChecksBuildErrorBeforeRecursing(t *testing.T) {
	src, err := os.ReadFile("clienttools.go")
	if err != nil {
		t.Fatalf("the file under test must be readable; a skipped check here is not a pass: %v", err)
	}

	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, "clienttools.go", src, 0)
	if perr != nil {
		t.Fatal(perr)
	}

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name.Name == "populateFromAllOf" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("populateFromAllOf not found: this test cannot say anything until it is")
	}

	// Find the `range schema.AllOf` loop specifically; the array branch above has the same shape and
	// already orders it correctly, so matching the wrong loop would pass vacuously.
	var loop *ast.RangeStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		r, ok := n.(*ast.RangeStmt)
		if !ok || loop != nil {
			return true
		}
		if sel, ok := r.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "AllOf" {
			loop = r
		}
		return true
	})
	if loop == nil {
		t.Fatal("the `range schema.AllOf` loop was not found; the structure changed and this test must " +
			"be rewritten rather than left to pass vacuously")
	}

	var errCheck, recurse token.Pos
	ast.Inspect(loop, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.IfStmt:
			// `if err != nil`
			if bin, ok := v.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if x, ok := bin.X.(*ast.Ident); ok && x.Name == "err" && errCheck == token.NoPos {
					errCheck = v.Pos()
				}
			}
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "populateFromAllOf" && recurse == token.NoPos {
				recurse = v.Pos()
			}
		}
		return true
	})

	if errCheck == token.NoPos {
		t.Fatal("no `if err != nil` inside the allOf loop: BuildSchema's error is not checked at all, " +
			"so a schema the library could not build is processed as if it were valid")
	}
	if recurse == token.NoPos {
		t.Fatal("no recursive populateFromAllOf call inside the allOf loop; the structure changed")
	}

	if int(errCheck) > int(recurse) {
		t.Errorf("the allOf loop recurses at line %d and only checks BuildSchema's error at line %d. "+
			"On error propSchema is nil, so this processes a schema the library just said it could not "+
			"build -- and before the nil guard was added, it panicked and took the controller pod down. "+
			"The array branch of this same function already checks first; this one must too",
			fset.Position(recurse).Line, fset.Position(errCheck).Line)
	}
}
