package domain

type ComparisonStatus string

const (
	StatusNoDrift   ComparisonStatus = "NO_DRIFT"
	StatusDrifted   ComparisonStatus = "DRIFTED"
	StatusUnmanaged ComparisonStatus = "UNMANAGED" // Found on platform, not in state
	StatusMissing   ComparisonStatus = "MISSING"   // Found in state, not on platform
	StatusError     ComparisonStatus = "ERROR"     // Error during comparison/fetching
)

type AttributeDiff struct {
	AttributeName string
	ExpectedValue any
	ActualValue   any
	Details       string // Optional extra context for the diff
}

type ComparisonResult struct {
	Status             ComparisonStatus
	ResourceKind       ResourceKind
	SourceIdentifier   string // From desired state (e.g., TF address)
	ProviderType       string
	ProviderAssignedID string // From actual state
	Differences        []AttributeDiff
	Error              error // Specific error during comparison for this resource
}
