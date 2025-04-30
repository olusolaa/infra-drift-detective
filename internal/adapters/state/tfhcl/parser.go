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
	resourceSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource", LabelNames: []string{"type", "name"}}}}
	foundAddresses := make(map[string]string)

	for path, file := range hclFiles {
		if file == nil || file.Body == nil {
			continue
		}
		content, contentDiags := file.Body.Content(resourceSchema)
		diags = append(diags, contentDiags...)

		for _, block := range content.Blocks {
			if block.Type == "resource" && len(block.Labels) == 2 {
				tfType := block.Labels[0]
				tfName := block.Labels[1]
				address := fmt.Sprintf("%s.%s", tfType, tfName)

				kind, err := mapping.MapTfTypeToDomainKind(tfType)
				if err != nil {
					continue
				}

				if kind == requestedKind {
					blockUniqueID := fmt.Sprintf("%s::%s", path, address)
					if firstPath, exists := foundAddresses[address]; exists {
						diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Duplicate resource address", Detail: fmt.Sprintf("Resource %s defined in %s and %s.", address, firstPath, path), Subject: &block.DefRange})
					} else {
						blocks = append(blocks, block)
						addresses[address] = blockUniqueID
						foundAddresses[address] = path
					}
				}
			}
		}
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
	return foundBlock, diags
}
