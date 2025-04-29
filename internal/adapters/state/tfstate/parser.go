package tfstate

import (
	"encoding/json"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"os"

	terraformjson "github.com/hashicorp/terraform-json"             // Using official parser library
	"github.com/olusolaa/infra-drift-detector/internal/core/domain" // Corrected import path
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

// parseStateFile reads and parses the Terraform state file.
func parseStateFile(filePath string) (*terraformjson.State, error) {
	stateFileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, fmt.Sprintf("failed to read state file: %s", filePath))
	}

	if len(stateFileBytes) == 0 {
		return nil, errors.New(errors.CodeStateParseError, fmt.Sprintf("state file is empty: %s", filePath))
	}

	var state terraformjson.State
	err = json.Unmarshal(stateFileBytes, &state)
	if err != nil {
		// Try parsing potentially older format (pre 0.12 had different structure slightly)
		// This might be too complex for now. Let's assume modern state format.
		return nil, errors.Wrap(err, errors.CodeStateParseError, fmt.Sprintf("failed to unmarshal state file JSON: %s", filePath))
	}

	if state.FormatVersion == "" {
		return nil, errors.New(errors.CodeStateParseError, "state file format version missing or invalid")
	}

	// Add more validation as needed (e.g., Terraform version compatibility?)

	return &state, nil
}

// findResourcesInState extracts resources of a specific kind from the parsed state.
// Handles resources within modules.
func findResourcesInState(state *terraformjson.State, requestedKind domain.ResourceKind) ([]*terraformjson.StateResource, error) {
	var foundResources []*terraformjson.StateResource

	if state.Values == nil || state.Values.RootModule == nil {
		// State might be empty or structured differently (e.g., legacy state)
		// For simplicity, assume modern structure with RootModule.Values
		return nil, nil // No resources found in expected structure
	}

	var collectResources func(module *terraformjson.StateModule)
	collectResources = func(module *terraformjson.StateModule) {
		if module == nil {
			return
		}
		for _, res := range module.Resources {
			if res == nil {
				continue
			}
			// Convert TF type (e.g., "aws_instance") to our domain Kind
			resourceKind, err := mapping.MapTfTypeToDomainKind(res.Type)
			if err != nil {
				// Log unsupported type? Skip for now.
				continue
			}

			if resourceKind == requestedKind {
				foundResources = append(foundResources, res)
			}
		}
		// Recursively check child modules
		for _, child := range module.ChildModules {
			collectResources(child)
		}
	}

	collectResources(state.Values.RootModule)

	return foundResources, nil
}
