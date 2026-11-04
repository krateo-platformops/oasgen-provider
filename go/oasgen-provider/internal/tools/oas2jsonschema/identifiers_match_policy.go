package oas2jsonschema

import (
	"fmt"
	"strings"
)

// warnIdentifiersMatchPolicyOmitted reports a findby that can adopt the WRONG object because the
// RestDefinition never said which of its identifiers have to match.
//
// identifiersMatchPolicy defaults to OR, and OR means the controller adopts the first item matching ANY
// ONE identifier: rest-dynamic-controller's isItemMatch returns true on the first identifier that hits,
// and findItemInList returns the first item isItemMatch is true for. With two or more identifiers, one of
// which is not unique across the collection, the adopted item is a different object than the resource
// names -- and the reconciler then treats that object AS this resource, which includes deleting it.
//
// That is measured, not hypothetical. On a live fleet a resource naming one child adopted its PARENT,
// reported Synced=True, and the parent was then deleted (#444).
//
// The default is deliberately NOT changed here. Flipping it to AND would silently re-point every existing
// resource that currently matches on a subset, and where a vendor's collection response is ABRIDGED --
// Ironic's node list carries 7 of roughly 40 fields -- AND over identifiers the collection does not even
// carry can never match, turning a wrong adoption into a permanent not-found and then a duplicate create.
// So this warns, names the field to set, and leaves the behaviour alone.
//
// # Why this does not fire on every multi-identifier findby
//
// Adopting the wrong item requires there to BE another item. A findby whose path fully identifies one
// object -- /orgs/{org}/actions/runner-groups/{runner_group_id} -- returns a single object, which the
// controller wraps as a list of exactly one, so OR has nothing else to pick and the omission is harmless
// there. The gate is therefore findByCanReturnMoreThanOneItem rather than the identifier count alone.
//
// A census of 2653 RestDefinitions established that the distinction matters rather than being pedantry,
// and it took four passes to get the question right. Of the definitions an identifier-count rule flags,
// two are single-object findbys that cannot mis-adopt, and the one genuine collection DELEGATES its
// observe, so its findby never runs either. **An ungated rule would have been wrong about every single
// one of its own hits.** A warning that fires only on provably safe input is not a cautious warning, it is
// a warning nobody will read by the time it matters.
//
// The gated rule fires on none of those three, which is the intended state rather than a sign it is inert:
// it is preventive. It catches the next author who declares this shape, and it catches an existing resource
// at the moment it becomes live -- generation re-runs on every RestDefinition change, so dropping
// observeApiRef or widening a findby from one object to a collection starts the warning then.
//
// It is NOT the case that nothing anywhere trips it. The kind that actually mis-adopted and deleted lives
// in a repository that is not checked out alongside these, and its maintainers confirmed the warning fires
// on it: an `{"nodes": [...]}` envelope, no itemsPath, five identifiers, policy absent. So the census bounds
// the FALSE-positive rate on what it could see, and says nothing about the true-positive rate -- a
// distinction worth preserving here, because "a census found nothing" reads as "there is nothing" to every
// later reader.
func (g *OASSchemaGenerator) warnIdentifiersMatchPolicyOmitted() error {
	identifiers := g.resourceConfig.Identifiers
	if len(identifiers) < 2 {
		// A single identifier (or none) cannot match "a subset": there is no second identifier for OR to
		// skip, so the policy makes no difference to which item is chosen.
		return nil
	}

	findByIndex := -1
	for i, v := range g.resourceConfig.Verbs {
		if strings.EqualFold(v.Action, ActionFindBy) {
			findByIndex = i
			break
		}
	}
	if findByIndex < 0 {
		// Identifier matching happens only on the findby path: isItemMatch is reached exclusively through
		// findItemInList, which has two call sites and both are findby's. A resource observed through
		// 'get' binds a path parameter instead, so the policy is inert for it.
		return nil
	}
	if g.resourceConfig.ObserveDelegated {
		// The findby is declared but never executed, so the policy cannot choose anything. See
		// ResourceConfig.ObserveDelegated for the four paths that each decline to reach it.
		//
		// This is not a weakening of the warning: generation re-runs when the RestDefinition changes, so
		// removing observeApiRef -- the moment the findby becomes live and the omission becomes dangerous
		// -- is exactly when the warning starts firing.
		return nil
	}
	findBy := g.resourceConfig.Verbs[findByIndex]
	if findBy.IdentifiersMatchPolicy != "" {
		// The author stated the policy. Whether they chose AND or OR, they chose, and OR declared
		// explicitly is a decision rather than an omission.
		return nil
	}

	schema, _, err := extractRawResponseSchemaForAction(g.docs, g.resourceConfig.Verbs, ActionFindBy, g.generatorConfig)
	if err != nil {
		// The response could not be read at all. That is an inability to PROVE the response is a single
		// object, not a proof that it is a collection -- and the asymmetry runs against staying quiet,
		// which would hide the hazard on exactly the documents that describe their responses least.
		// A findby whose response schema is missing is its own defect, tracked separately (#445).
		schema = nil
	}
	if !findByCanReturnMoreThanOneItem(schema, findBy.ItemsPath) {
		return nil
	}

	return SchemaGenerationError{
		Path: fmt.Sprintf("spec.resource.verbsDescription[%d].identifiersMatchPolicy", findByIndex),
		Code: CodeIdentifiersMatchPolicyOmitted,
		Message: fmt.Sprintf(
			"findby declares %d identifiers (%s) and no identifiersMatchPolicy, and its response can carry "+
				"more than one item, so the OR default applies: the FIRST item matching ANY ONE of those "+
				"identifiers is adopted as this resource, which may be a different object than the one it "+
				"names. Set identifiersMatchPolicy: AND to require all of them, or OR to state the current "+
				"behaviour deliberately",
			len(identifiers), strings.Join(identifiers, ", ")),
	}
}
