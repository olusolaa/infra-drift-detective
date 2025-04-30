package tfhcl

import (
	"context" // Use context if logger requires it
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports" // For logger
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	// Need evaluator error type if used directly
	// "github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
)

// parseAndMergeHCLDirectory reads all .tf and .tf.json files and merges them.
func parseAndMergeHCLDirectory(ctx context.Context, dirPath string, logger ports.Logger) (*hcl.File, hcl.Diagnostics, error) {
	var files []*hcl.File
	var allDiags hcl.Diagnostics
	parser := hclparse.NewParser()
	logger = logger.WithFields(map[string]any{"hcl_dir": dirPath})

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, nil, errors.Wrap(err, errors.CodeStateReadError, fmt.Sprintf("failed to read HCL directory: %s", dirPath))
	}

	foundHCLFiles := false
	logger.Debugf(ctx, "Scanning directory for HCL files")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileName := entry.Name()
		filePath := filepath.Join(dirPath, fileName)

		if strings.HasSuffix(fileName, ".tf") || strings.HasSuffix(fileName, ".tf.json") {
			foundHCLFiles = true
			fileLogger := logger.WithFields(map[string]any{"hcl_file": fileName})
			fileLogger.Debugf(ctx, "Parsing file")
			var file *hcl.File
			var diags hcl.Diagnostics
			if strings.HasSuffix(fileName, ".tf.json") {
				file, diags = parser.ParseJSONFile(filePath)
			} else {
				file, diags = parser.ParseHCLFile(filePath)
			}
			allDiags = append(allDiags, diags...)
			if file != nil {
				files = append(files, file)
			} else if diags.HasErrors() {
				fileLogger.Warnf(ctx, "Parsing failed:\n%s", diags.Error())
			} else {
				// Parser returned nil file without diags - log error
				fileLogger.Errorf(ctx, nil, "Internal HCL parsing error: Parser returned nil file without diagnostics")
				allDiags = allDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal HCL parsing error", Detail: "Parser returned nil file without diagnostics.", Subject: &hcl.Range{Filename: filePath}})
			}
		}
	}

	if !foundHCLFiles {
		return nil, nil, errors.New(errors.CodeStateParseError, fmt.Sprintf("no HCL files (.tf, .tf.json) found in directory: %s", dirPath))
	}
	// Check fatal errors *after* attempting all files
	if evaluator.DiagsHasFatalErrors(allDiags) { // Use helper from evaluator pkg
		return nil, allDiags, errors.New(errors.CodeStateParseError, "fatal errors encountered during HCL parsing")
	}

	// Merge the parsed files
	logger.Debugf(ctx, "Merging %d parsed HCL files", len(files))
	mergedBody := hcl.MergeFiles(files) // Note: MergeFiles is basic, doesn't handle overrides perfectly like TF core

	// Create a new file with the merged body and the proper schema that includes resource blocks
	mergedFile := &hcl.File{
		Body: &terraformFileBody{
			original: mergedBody,
		},
		Bytes: nil, // We don't have the merged bytes
	}

	if mergedFile == nil {
		// Should not happen if merging succeeded
		return nil, allDiags, errors.New(errors.CodeInternal, "HCL merging returned nil file")
	}

	logger.Debugf(ctx, "HCL merging complete.")
	return mergedFile, allDiags, nil // Return merged file and any non-fatal diagnostics
}

// terraformFileBody is a wrapper around hcl.Body that understands Terraform-specific
// block types including "resource" blocks
type terraformFileBody struct {
	original hcl.Body
}

// Content implements hcl.Body
func (tfb *terraformFileBody) Content(schema *hcl.BodySchema) (*hcl.BodyContent, hcl.Diagnostics) {
	// Use an open schema that accepts any block type, including 'resource' blocks
	openSchema := &hcl.BodySchema{
		Attributes: schema.Attributes,
		Blocks: append(schema.Blocks, hcl.BlockHeaderSchema{
			Type:       "resource",
			LabelNames: []string{"type", "name"},
		}, hcl.BlockHeaderSchema{
			Type: "locals",
		}, hcl.BlockHeaderSchema{
			// Wildcard block type
			Type:       "",
			LabelNames: nil,
		}),
	}

	// Log the block types we're expecting in our schema
	fmt.Printf("DEBUG: terraformFileBody.Content looking for blocks: %+v\n", schema.Blocks)

	content, diags := tfb.original.Content(openSchema)

	// Log the blocks that were found
	fmt.Printf("DEBUG: terraformFileBody.Content found %d blocks\n", len(content.Blocks))
	for i, block := range content.Blocks {
		fmt.Printf("DEBUG: Block[%d]: Type=%s, Labels=%v\n", i, block.Type, block.Labels)
	}

	// Filter diagnostics to remove "Unsupported block type" errors
	var filteredDiags hcl.Diagnostics
	for _, diag := range diags {
		if !strings.Contains(diag.Summary, "Unsupported block type") &&
			!strings.Contains(diag.Detail, "Blocks of type") &&
			!strings.Contains(diag.Detail, "are not expected here") {
			filteredDiags = append(filteredDiags, diag)
		}
	}

	return content, filteredDiags
}

// PartialContent implements hcl.Body
func (tfb *terraformFileBody) PartialContent(schema *hcl.BodySchema) (*hcl.BodyContent, hcl.Body, hcl.Diagnostics) {
	// Use a modified schema that allows any type of content
	enhancedSchema := &hcl.BodySchema{
		Attributes: schema.Attributes,
		Blocks: []hcl.BlockHeaderSchema{
			// Include wildcard block that matches any block type
			{Type: "", LabelNames: nil},
		},
	}

	content, remain, diags := tfb.original.PartialContent(enhancedSchema)

	// Filter out diagnostics about unexpected blocks
	var filteredDiags hcl.Diagnostics
	for _, diag := range diags {
		if !strings.Contains(diag.Summary, "Unexpected") && !strings.Contains(diag.Detail, "Blocks are not expected here") {
			filteredDiags = append(filteredDiags, diag)
		}
	}

	// Wrap the remaining body so it also understands Terraform blocks
	wrappedRemain := &terraformFileBody{original: remain}

	return content, wrappedRemain, filteredDiags
}

// JustAttributes implements hcl.Body
func (tfb *terraformFileBody) JustAttributes() (hcl.Attributes, hcl.Diagnostics) {
	return tfb.original.JustAttributes()
}

// MissingItemRange implements hcl.Body
func (tfb *terraformFileBody) MissingItemRange() hcl.Range {
	return tfb.original.MissingItemRange()
}

// enhanceSchemaWithTerraformBlocks adds Terraform-specific block types to a schema if they're not already present
func enhanceSchemaWithTerraformBlocks(schema *hcl.BodySchema) *hcl.BodySchema {
	if schema == nil {
		// Create a minimal schema that just includes resource blocks
		return &hcl.BodySchema{
			Blocks: []hcl.BlockHeaderSchema{
				{Type: "resource", LabelNames: []string{"type", "name"}},
				{Type: "locals"},
				// Wildcard block type that accepts any block type
				{Type: "", LabelNames: nil},
			},
		}
	}

	// Check if resource block is already in the schema
	hasResource := false
	hasLocals := false
	hasWildcard := false

	for _, block := range schema.Blocks {
		if block.Type == "resource" {
			hasResource = true
		}
		if block.Type == "locals" {
			hasLocals = true
		}
		if block.Type == "" {
			hasWildcard = true
		}
	}

	// Create a new schema with all the original blocks plus the ones we need
	enhanced := &hcl.BodySchema{
		Attributes: make([]hcl.AttributeSchema, len(schema.Attributes)),
		Blocks:     make([]hcl.BlockHeaderSchema, len(schema.Blocks)),
	}

	// Copy over all original schema elements
	copy(enhanced.Attributes, schema.Attributes)
	copy(enhanced.Blocks, schema.Blocks)

	// Add resource block if not already present
	if !hasResource {
		enhanced.Blocks = append(enhanced.Blocks, hcl.BlockHeaderSchema{
			Type:       "resource",
			LabelNames: []string{"type", "name"},
		})
	}

	// Add locals block if not already present
	if !hasLocals {
		enhanced.Blocks = append(enhanced.Blocks, hcl.BlockHeaderSchema{
			Type: "locals",
		})
	}

	// Add wildcard block if not already present
	if !hasWildcard {
		enhanced.Blocks = append(enhanced.Blocks, hcl.BlockHeaderSchema{
			Type:       "",
			LabelNames: nil,
		})
	}

	return enhanced
}

// findResourceBlocksInBody finds all resource blocks of a specific kind in a merged body.
func findResourceBlocksInBody(
	body hcl.Body,
	requestedKind domain.ResourceKind,
) (blocks []*hcl.Block, addresses map[string]string, diags hcl.Diagnostics) {

	blocks = make([]*hcl.Block, 0)
	addresses = make(map[string]string)

	fmt.Printf("DEBUG: findResourceBlocksInBody called for kind: %s\n", requestedKind)

	// Wrap the body to avoid "Unsupported block type" errors
	wrappedBody := &terraformFileBody{original: body}

	// Create a schema that specifically looks for resource blocks
	resourceSchema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       "resource",
				LabelNames: []string{"type", "name"},
			},
		},
	}

	content, contentDiags := wrappedBody.Content(resourceSchema)

	// Filter out "Unsupported block type" diagnostics
	for _, diag := range contentDiags {
		if !strings.Contains(diag.Summary, "Unsupported block type") &&
			!strings.Contains(diag.Detail, "Blocks of type") &&
			!strings.Contains(diag.Detail, "are not expected here") {
			diags = append(diags, diag)
		}
	}

	fmt.Printf("DEBUG: Processing %d resource blocks\n", len(content.Blocks))
	for _, block := range content.Blocks {
		if block.Type == "resource" && len(block.Labels) == 2 {
			tfType := block.Labels[0]
			tfName := block.Labels[1]
			address := fmt.Sprintf("%s.%s", tfType, tfName)
			fmt.Printf("DEBUG: Found resource %s (tfType=%s)\n", address, tfType)

			// Check if this resource type maps to the requested kind
			kind, err := mapping.MapTfTypeToDomainKind(tfType)
			if err != nil {
				// Skip resources with types we don't recognize
				fmt.Printf("DEBUG: Resource %s has unsupported type: %v\n", address, err)
				continue
			}

			fmt.Printf("DEBUG: Resource %s mapped to kind: %s (requested: %s)\n", address, kind, requestedKind)
			if kind == requestedKind {
				fmt.Printf("DEBUG: Resource %s matches requested kind, adding to result\n", address)
				if _, exists := addresses[address]; exists {
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Duplicate resource address",
						Detail:   fmt.Sprintf("Resource %s defined multiple times.", address),
						Subject:  &block.DefRange,
					})
					continue
				}
				blocks = append(blocks, block)
				addresses[address] = address // Map address -> address (key is unique address)
			} else {
				fmt.Printf("DEBUG: Resource %s does not match requested kind %s (found %s)\n", address, requestedKind, kind)
			}
		}
	}

	fmt.Printf("DEBUG: findResourceBlocksInBody returning %d blocks\n", len(blocks))
	return blocks, addresses, diags
}

// findSpecificResourceBlock finds a single resource block by TF address.
func findSpecificResourceBlock(body hcl.Body, identifier string) (*hcl.Block, hcl.Diagnostics) {
	var foundBlock *hcl.Block
	var diags hcl.Diagnostics

	// Wrap the body to avoid "Unsupported block type" errors
	wrappedBody := &terraformFileBody{original: body}

	// Create a schema that specifically looks for resource blocks
	resourceSchema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       "resource",
				LabelNames: []string{"type", "name"},
			},
		},
	}

	content, contentDiags := wrappedBody.Content(resourceSchema)

	// Filter out "Unsupported block type" diagnostics
	for _, diag := range contentDiags {
		if !strings.Contains(diag.Summary, "Unsupported block type") &&
			!strings.Contains(diag.Detail, "Blocks of type") &&
			!strings.Contains(diag.Detail, "are not expected here") {
			diags = append(diags, diag)
		}
	}

	parts := strings.SplitN(identifier, ".", 2)
	if len(parts) != 2 {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid resource identifier",
			Detail:   fmt.Sprintf("Expected 'type.name', got '%s'", identifier),
		})
		return nil, diags
	}
	expectedType, expectedName := parts[0], parts[1]

	for _, block := range content.Blocks {
		if block.Type == "resource" && len(block.Labels) == 2 {
			tfType := block.Labels[0]
			tfName := block.Labels[1]
			if tfType == expectedType && tfName == expectedName {
				if foundBlock != nil {
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Duplicate resource definition",
						Detail:   fmt.Sprintf("Resource %s defined multiple times.", identifier),
						Subject:  &block.DefRange,
					})
					return nil, diags
				}
				foundBlock = block
			}
		}
	}
	// No error if not found, caller handles that
	return foundBlock, diags
}
