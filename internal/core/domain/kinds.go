package domain

type ResourceKind string

const (
	KindComputeInstance  ResourceKind = "ComputeInstance"
	KindStorageBucket    ResourceKind = "StorageBucket"
	KindDatabaseInstance ResourceKind = "DatabaseInstance"
)

func (rk ResourceKind) String() string {
	return string(rk)
}
