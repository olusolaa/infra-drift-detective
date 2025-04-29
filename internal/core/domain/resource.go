package domain

type ResourceMetadata struct {
	Kind               ResourceKind
	ProviderType       string
	ProviderAssignedID string
	InternalID         string // Unique ID within this tool's run/context if needed
	SourceIdentifier   string // e.g., Terraform resource address like aws_instance.my_app
	Region             string
	AccountID          string // Optional, if available and useful
}

type PlatformResource interface {
	Metadata() ResourceMetadata
	Attributes() map[string]any
}

type StateResource interface {
	Metadata() ResourceMetadata
	Attributes() map[string]any
}
