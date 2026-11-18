package restclient

import (
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"gopkg.in/yaml.v3"
)

// AppliedDefault records one value this controller supplied from the vendor's own document, so a caller
// can log what it sent rather than discovering it in a packet capture.
type AppliedDefault struct {
	Name  string
	In    string
	Value string
}

// ApplyVendorRequiredDefaults fills a parameter the vendor declared `required` WITH a `default`, when
// nothing else supplied a value.
//
// THE DOCUMENT IS THE AUTHORITY. A `default` on a request parameter is usually read as "what the server
// assumes when the value is absent" -- but that reading is INCOHERENT with `required: true`, because a
// required parameter is never absent, so the server never gets to assume anything. The only coherent
// reading of the two together is the vendor telling a client: this must be present, and here is the value
// to send when you have no other. Declining to send it does not protect anybody; it ignores the document.
//
// WHAT THIS IS NOT, because the two look alike and the difference decided the design. 0.53.0 materialised
// `default: {}` on an optional object PARENT -- a default `crdgen` INVENTED because something beneath it
// was defaulted, which the apiserver then wrote into every CR. That was this controller asserting a value
// no vendor had written, it was irreversible, and 0.54.0 stopped it with `NoParentDefaults`. Here the
// default IS the vendor's, written by the vendor, on a parameter the vendor also marked required. One case
// is inventing a value; this one is reading one. Treating them alike was a mistake I made and corrected.
//
// REQUIRED ONLY, deliberately. On an OPTIONAL parameter the usual reading holds and the server really does
// supply the value, so omitting it is the faithful behaviour and sending it would assert a choice the
// author never made. The vendor is reflected in both cases -- that is the point -- and `required` is the
// bit that says which reflection.
//
// AN EXPLICIT VALUE ALWAYS WINS. A value already in the map came from the author, through
// verbsDescription[].headers, a fieldMapping, or a Configuration CR. This only fills an absence.
//
// The maps are mutated in place, which is what the callers want: all three sites hand the same maps to
// ValidateRequest immediately afterwards.
//
// NOT DONE INSIDE ValidateRequest, though that is where the failure surfaces: a function named Validate
// that silently changes its inputs is a trap for the next reader, and the dry-run caller at
// builder.go:533 asks "would this request be valid" -- a question that must be answered about the request
// as it will actually be sent, which is only true if the defaults are applied before it runs too.
func (u *UnstructuredClient) ApplyVendorRequiredDefaults(httpMethod, path string,
	parameters, query, headers, cookies map[string]string) []AppliedDefault {

	if u == nil || u.DocScheme == nil || u.DocScheme.Model.Paths == nil {
		return nil
	}
	_ = parameters // retained in the signature: the `path` case is deliberately not filled; see below.
	pathItem, ok := u.DocScheme.Model.Paths.PathItems.Get(path)
	if !ok {
		return nil
	}
	op, ok := pathItem.GetOperations().Get(strings.ToLower(httpMethod))
	if !ok {
		return nil
	}

	var applied []AppliedDefault
	// The same merge ValidateRequest uses, so the two cannot disagree about WHICH parameters exist: a
	// path-level `required` that the validator enforces but this never saw would fail every call with the
	// value sitting unread in the document (#361 was that defect one layer up).
	for _, param := range mergeParameters(pathItem.Parameters, op.Parameters) {
		if param == nil || param.Required == nil || !*param.Required {
			continue
		}
		var target map[string]string
		switch param.In {
		// `path` IS DELIBERATELY ABSENT, and this is the one place the document is not followed.
		//
		// A path parameter decides WHICH RESOURCE is addressed, so a value nobody supplied is not a
		// detail of the request -- it is a different request. Measured instance: two of one vendor's
		// documents declare
		//
		//	projectId   required: true   default: '*'
		//
		// so a consumer who does not supply it would address /projects/*/providers/... -- a read across
		// every project the credential can see, and the same default sits on an alertRules POST and
		// DELETE. That consumer is safe only because their own CRs supply projectId, which is a property
		// of their corpus and not of this code.
		//
		// A `..` segment can go further still and relocate the request HOST, verified on three channels.
		//
		// So: reflect the vendor wherever the blast radius is the request, and refuse where it is someone
		// else's tenant. The refusal is already loud and already correct --
		// "refusing to request %s: unresolved path parameter(s) %v" -- and an author who wants the
		// vendor's value can write it on the verb, where it is their decision rather than an inference.
		case "query":
			target = query
		case "header":
			target = headers
		case "cookie":
			target = cookies
		default:
			continue
		}
		if target == nil {
			continue
		}
		if _, present := target[param.Name]; present {
			continue
		}
		val, ok := scalarDefault(param)
		if !ok {
			continue
		}
		target[param.Name] = val
		applied = append(applied, AppliedDefault{Name: param.Name, In: param.In, Value: val})
	}
	return applied
}

// scalarDefault reads a parameter's default as the string a header, query, path or cookie can carry.
//
// A nil Schema is skipped rather than dereferenced: BuildSchema panics inside libopenapi on one, which
// this file already documents at another call site.
//
// A NON-SCALAR DEFAULT IS SKIPPED, not stringified. An object or array default cannot be placed in any of
// these four locations without inventing a serialisation the vendor never specified -- and inventing one
// would be the 0.53.0 mistake for real: asserting a shape nobody wrote. The parameter then stays absent
// and the existing "missing <in>: <name>" error fires, which is the honest outcome.
func scalarDefault(param *v3.Parameter) (string, bool) {
	if param.Schema == nil {
		return "", false
	}
	schema, err := param.Schema.BuildSchema()
	if err != nil || schema == nil || schema.Default == nil {
		return "", false
	}
	if schema.Default.Kind != yaml.ScalarNode {
		return "", false
	}
	// An explicitly null default is a declaration that there is no value, not a value of "null".
	if schema.Default.Tag == "!!null" {
		return "", false
	}
	return schema.Default.Value, true
}
