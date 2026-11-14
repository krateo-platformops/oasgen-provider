package restResources

import "github.com/krateo-platformops/unstructured-runtime/pkg/controller"

// If the handler stops satisfying ResultCreator, the runtime silently falls back to Create -- which
// discards the CreateResult and reinstates the phantom event. A compile-time assertion is the only
// thing that makes that failure loud.
var _ controller.ResultCreator = (*handler)(nil)
