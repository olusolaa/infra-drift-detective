package tfhcl

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

func ParseHCLDirectory(ctx context.Context, dirPath string, logger ports.Logger) (map[string]*hcl.File, hcl.Diagnostics, error) {
	files := make(map[string]*hcl.File)
	var allParsingDiags hcl.Diagnostics
	parser := hclparse.NewParser()
	logger = logger.WithFields(map[string]any{"hcl_dir": dirPath})

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, nil, errors.Wrap(err, errors.CodeStateReadError, fmt.Sprintf("failed to read HCL directory: %s", dirPath))
	}

	foundHCLFiles := false
	hasFatalParseError := false
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
			allParsingDiags = append(allParsingDiags, diags...)
			if evaluator.DiagsHasFatalErrors(diags) {
				hasFatalParseError = true
				fileLogger.Errorf(ctx, errors.New(errors.CodeStateParseError, diags.Error()), "Fatal parsing error")
			} else if file != nil {
				files[filePath] = file
			} else {
				fileLogger.Errorf(ctx, nil, "Internal HCL parsing error: Parser returned nil file without fatal diagnostics")
				allParsingDiags = allParsingDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal HCL parsing error", Detail: "Parser returned nil file without fatal diagnostics.", Subject: &hcl.Range{Filename: filePath}})
				hasFatalParseError = true
			}
		}
	}

	if !foundHCLFiles {
		return nil, nil, errors.New(errors.CodeStateParseError, fmt.Sprintf("no HCL files (.tf, .tf.json) found in directory: %s", dirPath))
	}
	if hasFatalParseError {
		return files, allParsingDiags, errors.New(errors.CodeStateParseError, "fatal errors encountered during HCL parsing")
	}

	logger.Debugf(ctx, "Parsed %d HCL files successfully.", len(files))
	return files, allParsingDiags, nil
}

func FindResourceBlocks(
	hclFiles map[string]*hcl.File,
	requestedKind domain.ResourceKind,
) (blocks []*hcl.Block, addresses map[string]string, diags hcl.Diagnostics) {

	blocks = make([]*hcl.Block, 0)
	addresses = make(map[string]string)
	resourceSchema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "resource", LabelNames: []string{"type", "name"}},
			{Type: "locals", LabelNames: []string{}},
			{Type: "variable", LabelNames: []string{"name"}},
			// Add other top-level blocks if necessary (e.g., provider, data, output)
		},
	}
	foundAddresses := make(map[string]string) // map[address] -> first path seen

	for path, file := range hclFiles {
		if file == nil || file.Body == nil {
			continue
		}

		content, contentDiags := file.Body.Content(resourceSchema)
		diags = append(diags, contentDiags...)
		if evaluator.DiagsHasFatalErrors(contentDiags) {
			// If fatal error during content extraction for this file, record it and continue to next file.
			// Do not stop processing other files entirely unless necessary.
			continue
		}

		for _, block := range content.Blocks {
			if block.Type == "resource" && len(block.Labels) == 2 {
				tfType := block.Labels[0]
				tfName := block.Labels[1]
				address := fmt.Sprintf("%s.%s", tfType, tfName)
				kind, err := mapping.MapTfTypeToDomainKind(tfType)
				if err != nil {
					// Log or add diagnostic for unmappable type? Maybe not here.
					continue
				}

				if kind == requestedKind {
					blockUniqueID := fmt.Sprintf("%s::%s", path, address)
					if firstPath, exists := foundAddresses[address]; exists {
						// Duplicate found! Add error diagnostic.
						diags = diags.Append(&hcl.Diagnostic{
							Severity: hcl.DiagError,
							Summary:  "Duplicate resource address",
							Detail:   fmt.Sprintf("Resource %s defined multiple times (first found in %s, also in %s).", address, firstPath, path),
							Subject:  &block.DefRange, // Subject points to the current (duplicate) block
						})

						// Remove the first instance from results if it's still there
						if _, addrExists := addresses[address]; addrExists {
							delete(addresses, address) // Remove from address map

							// Find and remove the block corresponding to the first instance (firstPath)
							originalBlockIndex := -1
							for i, b := range blocks {
								// Check if block 'b' corresponds to the first instance
								if len(b.Labels) == 2 && fmt.Sprintf("%s.%s", b.Labels[0], b.Labels[1]) == address && b.DefRange.Filename == firstPath {
									originalBlockIndex = i
									break
								}
							}
							if originalBlockIndex != -1 {
								// Remove block efficiently
								blocks = append(blocks[:originalBlockIndex], blocks[originalBlockIndex+1:]...)
							}
						}
						// Do not add the current block (the duplicate) or update foundAddresses/addresses.
						// Effectively removes the address from consideration entirely.

					} else {
						// First time seeing this address, add it.
						blocks = append(blocks, block)
						addresses[address] = blockUniqueID
						foundAddresses[address] = path // Track path for potential future duplicate detection
					}
				}
			}
		}
	}
	// Ensure final diags reflect fatal errors if any duplicates were found
	if evaluator.DiagsHasFatalErrors(diags) {
		// Potentially wrap or signal the fatal nature upstream if needed
	}
	return blocks, addresses, diags
}

func FindSpecificResourceBlock(hclFiles map[string]*hcl.File, identifier string) (*hcl.Block, hcl.Diagnostics) {
	var foundBlock *hcl.Block
	var firstPath string
	var diags hcl.Diagnostics
	resourceSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource", LabelNames: []string{"type", "name"}}}}

	parts := strings.SplitN(identifier, ".", 2)
	if len(parts) != 2 {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid resource identifier", Detail: fmt.Sprintf("Expected format 'type.name', got '%s'", identifier)})
		return nil, diags
	}
	expectedType, expectedName := parts[0], parts[1]

	for path, file := range hclFiles {
		if file == nil || file.Body == nil {
			continue
		}
		content, contentDiags := file.Body.Content(resourceSchema)
		diags = append(diags, contentDiags...)
		if evaluator.DiagsHasFatalErrors(contentDiags) {
			return nil, diags
		}

		for _, block := range content.Blocks {
			if block.Type == "resource" && len(block.Labels) == 2 {
				tfType := block.Labels[0]
				tfName := block.Labels[1]
				if tfType == expectedType && tfName == expectedName {
					if foundBlock != nil {
						diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Duplicate resource definition", Detail: fmt.Sprintf("Resource %s defined multiple times (found in %s and %s).", identifier, firstPath, path), Subject: &block.DefRange})
						return nil, diags
					}
					foundBlock = block
					firstPath = path
				}
			}
		}
	}
	if evaluator.DiagsHasFatalErrors(diags) {
		return nil, diags
	}
	return foundBlock, diags
}
