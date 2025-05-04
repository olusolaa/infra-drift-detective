package evaluator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

func parseHCLFiles(ctx context.Context, parser *hclparse.Parser, dirPath string, logger ports.Logger) (map[string]*hcl.File, hcl.Diagnostics, error) {
	files := make(map[string]*hcl.File)
	var allDiags hcl.Diagnostics

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, allDiags, apperrors.Wrap(err, apperrors.CodeStateReadError, fmt.Sprintf("failed to read HCL directory: %s", dirPath))
	}

	foundHCLFiles := false
	logger.Debugf(ctx, "Scanning directory for HCL files...")
	for _, entry := range entries {
		// Check context at the start of each iteration
		if err := ctx.Err(); err != nil {
			logger.Warnf(ctx, "Context cancelled during HCL file parsing loop")
			return files, allDiags, err
		}

		if entry.IsDir() || !isValidHCLFileName(entry.Name()) {
			continue
		}

		foundHCLFiles = true
		fileName := entry.Name()
		filePath := filepath.Join(dirPath, fileName)
		fileLogger := logger.WithFields(map[string]any{"hcl_file": fileName})
		fileLogger.Debugf(ctx, "Parsing file")

		var file *hcl.File
		var diags hcl.Diagnostics
		// File reading and parsing itself isn't context aware here
		if strings.HasSuffix(fileName, ".tf.json") {
			file, diags = parser.ParseJSONFile(filePath)
		} else {
			file, diags = parser.ParseHCLFile(filePath)
		}
		allDiags = append(allDiags, diags...)

		if file != nil {
			files[filePath] = file
		} else if !DiagsHasFatalErrors(diags) {
			allDiags = allDiags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError, Summary: "Internal HCL parsing error",
				Detail:  "Parser returned nil file without fatal diagnostics.",
				Subject: &hcl.Range{Filename: filePath},
			})
			fileLogger.Errorf(ctx, nil, "Internal HCL parsing error: nil file without fatal diagnostics")
		} else {
			fileLogger.Errorf(ctx, &HCLDiagnosticsError{Operation: "parsing file", FilePath: filePath, Diags: diags}, "Fatal parsing errors")
		}
	}

	if !foundHCLFiles {
		return nil, allDiags, apperrors.New(apperrors.CodeStateParseError, "no HCL files (.tf, .tf.json) found")
	}

	logger.Debugf(ctx, "Parsed %d HCL files.", len(files))
	return files, allDiags, nil
}

func FindResourceBlocksOfType(hclFiles map[string]*hcl.File, requestedKind domain.ResourceKind) ([]*hcl.Block, hcl.Diagnostics) {
	var blocks []*hcl.Block
	var diags hcl.Diagnostics
	resourceSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource", LabelNames: []string{"type", "name"}}}}

	for _, file := range hclFiles {
		if file == nil || file.Body == nil {
			continue
		}
		content, contentDiags := file.Body.Content(resourceSchema)
		diags = append(diags, contentDiags...)

		for _, block := range content.Blocks {
			if block.Type == "resource" && len(block.Labels) == 2 {
				tfType := block.Labels[0]
				kind, err := mapping.MapTfTypeToDomainKind(tfType)
				if err == nil && kind == requestedKind {
					blocks = append(blocks, block)
				}
			}
		}
	}
	return blocks, diags
}

func FindSpecificResourceBlock(hclFiles map[string]*hcl.File, identifier string) (*hcl.Block, hcl.Diagnostics) {
	var foundBlock *hcl.Block
	var firstPath string
	var diags hcl.Diagnostics
	resourceSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource", LabelNames: []string{"type", "name"}}}}

	parts := strings.SplitN(identifier, ".", 2)
	if len(parts) != 2 {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid resource identifier format", Detail: "Expected 'type.name'."})
		return nil, diags
	}
	expectedType, expectedName := parts[0], parts[1]

	for path, file := range hclFiles {
		if file == nil || file.Body == nil {
			continue
		}
		content, contentDiags := file.Body.Content(resourceSchema)
		diags = append(diags, contentDiags...)
		if DiagsHasFatalErrors(contentDiags) {
			return nil, diags
		}

		for _, block := range content.Blocks {
			if block.Type == "resource" && len(block.Labels) == 2 {
				if block.Labels[0] == expectedType && block.Labels[1] == expectedName {
					if foundBlock != nil {
						diags = diags.Append(&hcl.Diagnostic{
							Severity: hcl.DiagError, Summary: "Duplicate resource definition",
							Detail:  fmt.Sprintf("Resource %s defined in %s and %s.", identifier, firstPath, path),
							Subject: &block.DefRange,
						})
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

func isValidHCLFileName(name string) bool {
	return strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json")
}
