package domain

type ComparisonStatus string

const (
	StatusNoDrift   ComparisonStatus = "NO_DRIFT"
	StatusDrifted   ComparisonStatus = "DRIFTED"
	StatusUnmanaged ComparisonStatus = "UNMANAGED"
	StatusMissing   ComparisonStatus = "MISSING"
	StatusError     ComparisonStatus = "ERROR"
)

type AttributeDiff struct {
	AttributeName string
	ExpectedValue any
	ActualValue   any
	Details       string
}

type ComparisonResult struct {
	Status             ComparisonStatus
	ResourceKind       ResourceKind
	SourceIdentifier   string
	ProviderType       string
	ProviderAssignedID string
	Differences        []AttributeDiff
	Error              error
}
