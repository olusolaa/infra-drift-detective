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
			if file != nil {
				files[filePath] = file
			} else if diags.HasErrors() {
				fileLogger.Warnf(ctx, "Parsing failed:\n%s", diags.Error())
			} else {
				fileLogger.Errorf(ctx, nil, "Internal HCL parsing error: Parser returned nil file without diagnostics")
				allParsingDiags = allParsingDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal HCL parsing error", Detail: "Parser returned nil file without diagnostics.", Subject: &hcl.Range{Filename: filePath}})
			}
		}
	}

	if !foundHCLFiles {
		return nil, nil, errors.New(errors.CodeStateParseError, fmt.Sprintf("no HCL files (.tf, .tf.json) found in directory: %s", dirPath))
	}
	if evaluator.DiagsHasFatalErrors(allParsingDiags) {
		return files, allParsingDiags, errors.New(errors.CodeStateParseError, "fatal errors encountered during HCL parsing")
	}

	logger.Debugf(ctx, "Parsed %d HCL files.", len(files))
	return files, allParsingDiags, nil
}

func FindResourceBlocks(
	hclFiles map[string]*hcl.File,
	requestedKind domain.ResourceKind,
) (blocks []*hcl.Block, addresses map[string]string, diags hcl.Diagnostics) {

	blocks = make([]*hcl.Block, 0)
	addresses = make(map[string]string)
	resourceSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource", LabelNames: []string{"type", "name"}}}}

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
					if _, exists := addresses[address]; exists {
						diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Duplicate resource address", Detail: fmt.Sprintf("Resource %s defined multiple times (found again in %s).", address, path), Subject: &block.DefRange})
						continue
					}
					blocks = append(blocks, block)
					addresses[address] = blockUniqueID
				}
			}
		}
	}
	return blocks, addresses, diags
}

func FindSpecificResourceBlock(hclFiles map[string]*hcl.File, identifier string) (*hcl.Block, hcl.Diagnostics) {
	var foundBlock *hcl.Block
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
						diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Duplicate resource definition", Detail: fmt.Sprintf("Resource %s defined multiple times (found in %s and %s).", identifier, foundBlock.DefRange.Filename, path), Subject: &block.DefRange})
						return nil, diags
					}
					foundBlock = block
				}
			}
		}
	}
	return foundBlock, diags
}
