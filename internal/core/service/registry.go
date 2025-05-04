package service

import (
	"fmt"
	"sync"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

type ComponentRegistry struct {
	mu                sync.RWMutex
	stateProviders    map[string]ports.StateProvider
	platformProviders map[string]ports.PlatformProvider
	resourceComparers map[domain.ResourceKind]ports.ResourceComparer
}

func NewComponentRegistry() *ComponentRegistry {
	return &ComponentRegistry{
		stateProviders:    make(map[string]ports.StateProvider),
		platformProviders: make(map[string]ports.PlatformProvider),
		resourceComparers: make(map[domain.ResourceKind]ports.ResourceComparer),
	}
}

func (r *ComponentRegistry) RegisterStateProvider(provider ports.StateProvider) error {
	if provider == nil {
		return errors.New(errors.CodeInternal, "attempted to register nil state provider")
	}
	providerType := provider.Type()
	if providerType == "" {
		return errors.New(errors.CodeInternal, "state provider type cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.stateProviders[providerType]; exists {
		return errors.New(errors.CodeInternal, fmt.Sprintf("state provider type '%s' already registered", providerType))
	}
	r.stateProviders[providerType] = provider
	return nil
}

func (r *ComponentRegistry) GetStateProvider(providerType string) (ports.StateProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, exists := r.stateProviders[providerType]
	if !exists {
		return nil, errors.New(errors.CodeConfigValidation, fmt.Sprintf("state provider type '%s' not found", providerType))
	}
	return provider, nil
}

func (r *ComponentRegistry) RegisterPlatformProvider(provider ports.PlatformProvider) error {
	if provider == nil {
		return errors.New(errors.CodeInternal, "attempted to register nil platform provider")
	}
	providerType := provider.Type()
	if providerType == "" {
		return errors.New(errors.CodeInternal, "platform provider type cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.platformProviders[providerType]; exists {
		return errors.New(errors.CodeInternal, fmt.Sprintf("platform provider type '%s' already registered", providerType))
	}
	r.platformProviders[providerType] = provider
	return nil
}

func (r *ComponentRegistry) GetPlatformProvider(providerType string) (ports.PlatformProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, exists := r.platformProviders[providerType]
	if !exists {
		return nil, errors.New(errors.CodeConfigValidation, fmt.Sprintf("platform provider type '%s' not found", providerType))
	}
	return provider, nil
}

func (r *ComponentRegistry) RegisterResourceComparer(comparer ports.ResourceComparer) error {
	if comparer == nil {
		return errors.New(errors.CodeInternal, "attempted to register nil resource comparer")
	}
	kind := comparer.Kind()
	if kind == "" {
		return errors.New(errors.CodeInternal, "resource comparer kind cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.resourceComparers[kind]; exists {
		return errors.New(errors.CodeInternal, fmt.Sprintf("resource comparer for kind '%s' already registered", kind))
	}
	r.resourceComparers[kind] = comparer
	return nil
}

func (r *ComponentRegistry) GetResourceComparer(kind domain.ResourceKind) (ports.ResourceComparer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	comparer, exists := r.resourceComparers[kind]
	if !exists {
		return nil, errors.New(errors.CodeNotImplemented, fmt.Sprintf("resource comparer for kind '%s' not implemented", kind))
	}
	return comparer, nil
}
