package tfstate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

type (
	State struct {
		Version          int        `json:"version"`
		TerraformVersion string     `json:"terraform_version"`
		Serial           int        `json:"serial"`
		Lineage          string     `json:"lineage"`
		Resources        []Resource `json:"resources"`
	}

	Resource struct {
		Module    string     `json:"module,omitempty"`
		Mode      string     `json:"mode"`
		Type      string     `json:"type"`
		Name      string     `json:"name"`
		Provider  string     `json:"provider"`
		Instances []Instance `json:"instances"`
	}

	Instance struct {
		SchemaVersion int            `json:"schema_version"`
		Attributes    map[string]any `json:"attributes"`
		Private       string         `json:"private"`
		Dependencies  []string       `json:"dependencies"`
	}
)

type stateParser struct {
	filePath   string
	stateCache *State
	parseErr   error
	mutex      sync.RWMutex
	logger     ports.Logger
}

func newStateParser(path string, logger ports.Logger) *stateParser {
	return &stateParser{
		filePath: path,
		logger:   logger.WithFields(map[string]any{"component": "tfstate_parser", "file_path": path}),
	}
}

func (sp *stateParser) parseAndCache(ctx context.Context) (*State, error) {
	sp.mutex.RLock()
	if sp.stateCache != nil || sp.parseErr != nil {
		defer sp.mutex.RUnlock()
		return sp.stateCache, sp.parseErr
	}
	sp.mutex.RUnlock()

	sp.mutex.Lock()
	defer sp.mutex.Unlock()

	if sp.stateCache != nil || sp.parseErr != nil {
		return sp.stateCache, sp.parseErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	raw, err := os.ReadFile(sp.filePath)
	if err != nil {
		sp.parseErr = errors.Wrap(err, errors.CodeStateReadError, "failed to read state file")
		return nil, sp.parseErr
	}
	if len(raw) == 0 {
		sp.parseErr = errors.NewUserFacing(errors.CodeStateParseError, "state file is empty", "")
		return nil, sp.parseErr
	}

	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		sp.parseErr = errors.WrapUserFacing(err, errors.CodeStateParseError, "invalid JSON in state", "")
		return nil, sp.parseErr
	}
	if state.Version < 3 {
		sp.parseErr = errors.NewUserFacing(
			errors.CodeUnsupportedStateVersion,
			fmt.Sprintf("unsupported state version %d (only v4 and v5 supported)", state.Version),
			"Upgrade or downgrade Terraform if needed and regenerate state.")
		return nil, sp.parseErr
	}

	sp.stateCache = &state
	return sp.stateCache, nil
}

func findResourcesInState(
	state *State,
	kind domain.ResourceKind,
	_ ports.Logger,
) ([]*Resource, error) {
	if state == nil {
		return nil, nil
	}
	var out []*Resource
	for i := range state.Resources {
		r := &state.Resources[i]
		if r.Mode != "managed" {
			continue
		}
		k, err := mapping.MapTfTypeToDomainKind(r.Type)
		if err == nil && k == kind {
			out = append(out, r)
		}
	}
	return out, nil
}

func findSpecificResource(state *State, kind domain.ResourceKind, identifier string, _ ports.Logger) (*Resource, error) {
	if state == nil {
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("state is nil, resource '%s' not found", identifier))
	}

	for i := range state.Resources {
		r := &state.Resources[i]
		if r.Mode != "managed" {
			continue
		}
		if buildResourceAddress(r) != identifier {
			continue
		}
		k, err := mapping.MapTfTypeToDomainKind(r.Type)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, fmt.Sprintf("unmappable resource type %q", r.Type))
		}
		if k != kind {
			return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' found, but it has kind '%s', expected '%s'", identifier, k, kind))
		}
		return r, nil
	}
	return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' of kind '%s' not found", identifier, kind))
}

func buildResourceAddress(r *Resource) string {
	if r.Module != "" {
		return r.Module + "." + r.Type + "." + r.Name
	}
	return r.Type + "." + r.Name
}

func FindRelatedResources(state *State, baseResource *Resource) map[string][]*Resource {
	if state == nil || baseResource == nil || len(baseResource.Instances) == 0 {
		return nil
	}

	related := make(map[string][]*Resource)
	baseType := baseResource.Type
	baseName := baseResource.Name
	basePrefix := strings.Split(baseType, "_")[0]

	var baseID string
	if len(baseResource.Instances) > 0 && baseResource.Instances[0].Attributes != nil {
		if id, ok := baseResource.Instances[0].Attributes["id"].(string); ok {
			baseID = id
		}
	}

	for i := range state.Resources {
		res := &state.Resources[i]
		if res.Type == baseType && res.Name == baseName {
			continue
		}

		relationType := ""

		if strings.HasPrefix(res.Type, baseType+"_") {
			relationType = strings.TrimPrefix(res.Type, baseType+"_")
		} else if strings.HasPrefix(res.Type, basePrefix+"_") &&
			len(res.Instances) > 0 &&
			res.Instances[0].Attributes != nil {

			for attrKey, attrVal := range res.Instances[0].Attributes {
				if (attrKey == basePrefix+"_bucket" || attrKey == "bucket") && baseID != "" {
					if bucketRef, ok := attrVal.(string); ok && bucketRef == baseID {
						relationType = strings.TrimPrefix(res.Type, basePrefix+"_")
						break
					}
				}
			}
		}

		if relationType != "" {
			related[relationType] = append(related[relationType], res)
		}
	}

	return related
}
