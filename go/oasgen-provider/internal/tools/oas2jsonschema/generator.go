package oas2jsonschema

import (
	"fmt"
)

// OASSchemaGenerator orchestrates the generation of CRD schemas from an OpenAPI document.
type OASSchemaGenerator struct {
	generatorConfig *GeneratorConfig
	resourceConfig  *ResourceConfig
	// docs resolves which document each verb's path belongs to. While #108 is unimplemented it holds a
	// single document and every verb resolves to it.
	//
	// The ONLY document field. An earlier revision of this seam kept `doc` alongside it for the
	// resource-level reads, and the two promptly disagreed: tests construct this struct literally, so
	// `docs` was nil while `doc` was set, and the nil-safe For() returned nil rather than panicking --
	// which moved the panic one line later into FindPath. Two fields that must agree is the same shape
	// that produced the 0.28.0 readiness loop. Resource-level reads go through doc() below.
	docs *DocumentSet
	// skippedSecuritySchemes records security schemes the generator could not express, populated during
	// configuration-schema generation. Surfaced by the caller as a warning and a condition: a resource whose
	// only scheme was skipped has no way to authenticate, and today that is discovered via 401s.
	skippedSecuritySchemes []string
}

// doc is the document named by spec.oasPath, for the reads that are a property of the RESOURCE rather
// than of one verb: the security schemes and the CRD version.
func (g *OASSchemaGenerator) doc() OASDocument { return g.docs.Default() }

// SkippedSecuritySchemes returns the security schemes that could not be generated, as
// "<name> (type: <type>, in: <in>)". Empty unless GenerateConfigurationSchema has run.
func (g *OASSchemaGenerator) SkippedSecuritySchemes() []string { return g.skippedSecuritySchemes }

// NewOASSchemaGenerator creates a new, configured OASSchemaGenerator.
func NewOASSchemaGenerator(doc OASDocument, config *GeneratorConfig, resourceConfig *ResourceConfig) *OASSchemaGenerator {
	return NewOASSchemaGeneratorFromSet(NewDocumentSet(doc), config, resourceConfig)
}

// NewOASSchemaGeneratorFromSet is the form that takes a document set directly.
//
// Kept alongside the single-document constructor so the many existing callers and tests are untouched by
// the seam: they pass one document, it becomes a set of one, and nothing about their behaviour changes.
func NewOASSchemaGeneratorFromSet(docs *DocumentSet, config *GeneratorConfig, resourceConfig *ResourceConfig) *OASSchemaGenerator {
	return &OASSchemaGenerator{
		generatorConfig: config,
		resourceConfig:  resourceConfig,
		docs:            docs,
	}
}

// Generate orchestrates the full schema (spec + status) generation process along with configuration schema if needed.
func (g *OASSchemaGenerator) Generate() (*GenerationResult, error) {
	var generationWarnings []error

	// Generate Spec Schema
	specSchema, warnings, err := g.BuildSpecSchema()
	if err != nil {
		// Fatal error
		return nil, fmt.Errorf("failed to generate spec schema: %w", err)
	}
	generationWarnings = append(generationWarnings, warnings...)

	// Generate Status Schema
	statusSchema, warnings, err := g.BuildStatusSchema()
	if err != nil {
		// A failure to generate status schema is currently not considered a fatal error for compatibility reasons
		generationWarnings = append(generationWarnings, fmt.Errorf("failed to generate status schema: %w", err))
	}
	generationWarnings = append(generationWarnings, warnings...)

	// Validate Status Schema
	validationWarnings := ValidateSchemas(g.docs, g.resourceConfig.Verbs, g.generatorConfig)

	// Generate Configuration Schema if needed
	var configurationSchema []byte
	if len(g.resourceConfig.ConfigurationFields) > 0 || len(g.doc().SecuritySchemes()) > 0 {
		var err error
		configurationSchema, err = g.BuildConfigurationSchema()
		if err != nil {
			// Fatal error since configuration schema is required in these cases
			return nil, fmt.Errorf("failed to generate configuration schema: %w", err)
		}
	}

	// Annotate schemas to disambiguate duplicate field names.
	// This is necessary due to the underlying tool for generating CRDs.
	//finalSpec, finalStatus, err := annotateSchemas(specSchema, statusSchema, "x-crdgen-identifier-name")
	//if err != nil {
	//	return nil, fmt.Errorf("failed to annotate schemas with 'x-crdgen-identifier-name': %w", err)
	//}
	//
	//// Annotate configuration schema if exists, to disambiguate duplicate field names.
	//// This is necessary due to the underlying tool for generating CRDs.
	//var finalConfig []byte
	//if len(configurationSchema) > 0 {
	//	var err error
	//	finalConfig, _, err = annotateSchemas(configurationSchema, nil, "x-crdgen-identifier-name")
	//	if err != nil {
	//		return nil, fmt.Errorf("failed to annotate configuration schema with 'x-crdgen-identifier-name': %w", err)
	//	}
	//}

	// TODO: consider to log the generated spec schema for debugging purposes (we need the logger setup)
	//log.Print("======= Final Spec Schema =======")
	//log.Print(string(specSchema))
	//log.Print("======= End Spec Schema =======")

	// TODO: consider to log the generated status schema for debugging purposes (we need the logger setup)
	//log.Print("======= Final Status Schema  =======")
	//log.Print(string(statusSchema))
	//log.Print("======= End Status Schema =======")

	// TODO: consider to log the generated configuration schema for debugging purposes (we need the logger setup)
	//log.Print("Final configuration schema")
	//if configurationSchema != nil {
	//	log.Print(string(configurationSchema))
	//}
	//log.Print("======= End Configuration Schema =======")

	return &GenerationResult{
		SpecSchema:             specSchema,
		StatusSchema:           statusSchema,
		ConfigurationSchema:    configurationSchema,
		GenerationWarnings:     generationWarnings,
		SkippedSecuritySchemes: g.skippedSecuritySchemes,
		ValidationWarnings:     validationWarnings,
	}, nil
}
