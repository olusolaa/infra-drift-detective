package domain

type ResourceKind string

const (
	KindComputeInstance  ResourceKind = "ComputeInstance"
	KindStorageBucket    ResourceKind = "StorageBucket"
	KindDatabaseInstance ResourceKind = "DatabaseInstance"
	// Add other resource kinds as needed
)

func (rk ResourceKind) String() string {
	return string(rk)
}
