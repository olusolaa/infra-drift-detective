package tfhcl

import (
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
)

type parsedFile struct {
	hclFile *hcl.File
	err     error
}

func parseHCLDirectory(dirPath string) (map[string]*hcl.File, error) {
	files := make(map[string]*hcl.File)
	parser := hclparse.NewParser()

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, fmt.Sprintf("failed to read HCL directory: %s", dirPath))
	}

	results := make(chan parsedFile, len(entries))

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileName := entry.Name()
		filePath := filepath.Join(dirPath, fileName)

		if strings.HasSuffix(fileName, ".tf") || strings.HasSuffix(fileName, ".tf.json") {
			count++
			go func(fPath string) {
				var file *hcl.File
				var diags hcl.Diagnostics
				if strings.HasSuffix(fileName, ".tf.json") {
					file, diags = parser.ParseJSONFile(fPath)
				} else {
					file, diags = parser.ParseHCLFile(fPath)
				}
				if diags.HasErrors() {
					results <- parsedFile{err: errors.New(errors.CodeStateParseError, fmt.Sprintf("HCL parsing errors in %s: %s", fPath, diags.Error()))}
					return
				}
				results <- parsedFile{hclFile: file}
			}(filePath)
		}
	}

	var firstErr error
	for i := 0; i < count; i++ {
		res := <-results
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		if res.hclFile != nil && res.hclFile.Body != nil {
			files[filePath] = res.hclFile
		}
	}
	close(results)

	if firstErr != nil {
		return files, firstErr
	}
	if len(files) == 0 {
		return nil, errors.New(errors.CodeStateParseError, fmt.Sprintf("no valid HCL files (.tf, .tf.json) found in directory: %s", dirPath))
	}

	return files, nil
}

func findHCLResources(
	hclFiles map[string]*hcl.File,
	requestedKind domain.ResourceKind,
) ([]*hcl.Block, map[string]string, error) {
	resources := make([]*hcl.Block, 0)
	addresses := make(map[string]string)

	resourceSchema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "resource", LabelNames: []string{"type", "name"}},
		},
	}

	for path, file := range hclFiles {
		content, diags := file.Body.Content(resourceSchema)
		if diags.HasErrors() {
			return nil, nil, errors.New(errors.CodeStateParseError, fmt.Sprintf("HCL content error in %s: %s", path, diags.Error()))
		}

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
					blockIdentifier := fmt.Sprintf("%s:%s", path, address)
					resources = append(resources, block)
					addresses[blockIdentifier] = address
				}
			}
		}
	}

	return resources, addresses, nil
}

// mapTfTypeToDomainKind needs to be accessible here too.
// Consider moving this mapping logic to a shared internal package,
// e.g., internal/adapters/terraform/common/mappings.go
// For now, we might need to duplicate or pass it around. Let's assume it's available.

func extractLiteralAttributes(blockBody hcl.Body) (map[string]any, error) {
	attrs, diags := blockBody.JustAttributes()
	if diags.HasErrors() {
	}

	extracted := make(map[string]any)
	if attrs == nil {
		return extracted, nil
	}

	for name, attr := range attrs {
		val, valDiags := attr.Expr.Value(nil)
		if valDiags.HasErrors() {
			continue
		}

		var nativeValue any
		switch val.Type() {
		case cty.String:
			err := gocty.FromCtyValue(val, &nativeValue)
			if err == nil {
				extracted[name] = nativeValue
			}
		case cty.Number:
			var intVal int64
			var floatVal float64
			if err := gocty.FromCtyValue(val, &intVal); err == nil {
				extracted[name] = int(intVal)
			} else if err := gocty.FromCtyValue(val, &floatVal); err == nil {
				extracted[name] = floatVal
			}
		case cty.Bool:
			err := gocty.FromCtyValue(val, &nativeValue)
			if err == nil {
				extracted[name] = nativeValue
			}
		default:
			continue
		}
	}

	return extracted, nil
}
