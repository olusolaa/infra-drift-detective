package domain

import "context"

type ResourceMetadata struct {
	Kind               ResourceKind
	ProviderType       string
	ProviderAssignedID string
	InternalID         string
	SourceIdentifier   string // e.g., Terraform resource address like aws_instance.my_app
	Region             string
	AccountID          string
}

//go:generate mockery --name=PlatformResource --output=./mocks --outpkg=mocks --case underscore
type PlatformResource interface {
	Metadata() ResourceMetadata
	Attributes(ctx context.Context) (map[string]any, error)
}

//go:generate mockery --name=StateResource --output=./mocks --outpkg=mocks --case underscore
type StateResource interface {
	Metadata() ResourceMetadata
	Attributes() map[string]any
}
