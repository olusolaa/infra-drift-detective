package service

import (
	"context"
	"sync"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"golang.org/x/sync/errgroup"
)

// EngineRunConfig holds specific configuration needed for an engine run,
// derived from the main application configuration and CLI overrides.
type EngineRunConfig struct {
	ResourceKindsToProcess []domain.ResourceKind
	AttributesToCheck      map[domain.ResourceKind][]string
	Concurrency            int
}

// DriftAnalysisEngine orchestrates the drift detection process.
// It depends on interfaces (ports) for external interactions (providers, matcher, reporter)
// and uses a concurrent pipeline model based on channels.
type DriftAnalysisEngine struct {
	registry         *ComponentRegistry
	matcher          ports.Matcher
	reporter         ports.Reporter
	logger           ports.Logger
	runConfig        EngineRunConfig
	stateProvider    ports.StateProvider
	platformProvider ports.PlatformProvider
}

// NewDriftAnalysisEngine creates a new engine instance, injecting dependencies.
func NewDriftAnalysisEngine(
	registry *ComponentRegistry,
	matcher ports.Matcher,
	reporter ports.Reporter,
	logger ports.Logger,
	runConfig EngineRunConfig,
	stateProvider ports.StateProvider,
	platformProvider ports.PlatformProvider,
) (*DriftAnalysisEngine, error) {
	// Apply default concurrency if not set or invalid
	if runConfig.Concurrency <= 0 {
		runConfig.Concurrency = 10
	}
	// Validate essential dependencies
	if stateProvider == nil {
		return nil, errors.New(errors.CodeConfigValidation, "state provider cannot be nil")
	}
	if platformProvider == nil {
		return nil, errors.New(errors.CodeConfigValidation, "platform provider cannot be nil")
	}
	if len(runConfig.ResourceKindsToProcess) == 0 {
		return nil, errors.New(errors.CodeConfigValidation, "no resource kinds specified for processing")
	}

	return &DriftAnalysisEngine{
		registry:         registry,
		matcher:          matcher,
		reporter:         reporter,
		logger:           logger,
		runConfig:        runConfig,
		stateProvider:    stateProvider,
		platformProvider: platformProvider,
	}, nil
}

// Run executes the multi-stage drift analysis workflow concurrently.
// It sets up a pipeline using channels and manages goroutines with an errgroup.
func (e *DriftAnalysisEngine) Run(ctx context.Context) error {
	e.logger.Infof(ctx, "Starting drift analysis run using %s state and %s platform providers",
		e.stateProvider.Type(), e.platformProvider.Type())

	// --- Setup Workflow Channels ---
	desiredChan := make(chan domain.StateResource, 100)
	actualChan := make(chan domain.PlatformResource, 100)
	matchResultChan := make(chan ports.MatchingResult, 1) // Only one result expected
	compareInputChan := make(chan ports.MatchedPair, 100)
	comparisonResultChan := make(chan domain.ComparisonResult, 100)

	// --- Setup Concurrency Management ---
	g, childCtx := errgroup.WithContext(ctx) // Use errgroup for context cancellation propagation
	var finalResults []domain.ComparisonResult
	var finalResultsMutex sync.Mutex // Protect concurrent writes to finalResults

	// --- Launch Workflow Stages as Goroutines ---

	// Stage 1a: List desired resources. Reads from state provider, sends to desiredChan.
	g.Go(func() error { return e.stageListDesired(childCtx, desiredChan) })

	// Stage 1b: List actual resources. Reads from platform provider, sends to actualChan.
	g.Go(func() error { return e.stageListActual(childCtx, actualChan) })

	// Stage 2: Match resources. Collects from desiredChan & actualChan, sends results to matchResultChan.
	g.Go(func() error { return e.stageMatchResources(childCtx, desiredChan, actualChan, matchResultChan) })

	// Stage 3: Dispatch comparisons. Reads match results, processes unmatched, sends matched pairs to compareInputChan.
	g.Go(func() error {
		return e.stageDispatchComparisons(childCtx, matchResultChan, compareInputChan, &finalResults, &finalResultsMutex)
	})

	// Stage 4: Compare resources. Launches worker pool reading from compareInputChan, sends results to comparisonResultChan.
	g.Go(func() error { return e.stageCompareResources(childCtx, compareInputChan, comparisonResultChan) })

	// Stage 5: Aggregate results. Reads from comparisonResultChan, appends to finalResults.
	g.Go(func() error {
		return e.stageAggregateResults(childCtx, comparisonResultChan, &finalResults, &finalResultsMutex)
	})

	// --- Wait for all stages and handle potential errors ---
	if runErr := g.Wait(); runErr != nil {
		// Log appropriately depending on whether it was cancellation or another error
		if runErr == context.Canceled || runErr == context.DeadlineExceeded {
			e.logger.Warnf(ctx, "Drift analysis workflow cancelled or timed out: %v", runErr)
		} else {
			e.logger.Errorf(ctx, runErr, "drift analysis workflow encountered an error")
		}
		// Attempt to report any results gathered before the error occurred
		if len(finalResults) > 0 && runErr != context.Canceled && runErr != context.DeadlineExceeded {
			e.reportResults(ctx, finalResults) // Use helper
		}
		return runErr // Propagate the first error encountered
	}

	// --- Stage 6: Report Final Results if workflow completed successfully ---
	e.logger.Infof(ctx, "Drift analysis workflow completed successfully.")
	reportErr := e.reportResults(ctx, finalResults) // Use helper
	if reportErr != nil {
		return reportErr // Return reporting error
	}

	e.logger.Infof(ctx, "Drift analysis run finished successfully.")
	return nil
}

// --- Stage Helper Functions ---

// stageListDesired lists resources from the configured state provider for all configured kinds.
func (e *DriftAnalysisEngine) stageListDesired(ctx context.Context, desiredChan chan<- domain.StateResource) error {
	defer close(desiredChan) // Ensure channel is closed when listing is done or errors out
	for _, kind := range e.runConfig.ResourceKindsToProcess {
		// Check for context cancellation before processing each kind
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.logger.Debugf(ctx, "[Stage 1a] Listing desired resources of kind: %s", kind)
		resources, err := e.stateProvider.ListResources(ctx, kind)
		if err != nil {
			// Wrap and log provider error, then return to signal errgroup
			wrappedErr := errors.Wrap(err, errors.CodeStateReadError, "failed listing desired resources")
			e.logger.Errorf(ctx, wrappedErr, "error listing desired kind %s", kind)
			return wrappedErr
		}
		e.logger.Debugf(ctx, "[Stage 1a] Found %d desired resources of kind: %s", len(resources), kind)
		// Send found resources to the channel, checking for cancellation
		for _, res := range resources {
			select {
			case desiredChan <- res:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	e.logger.Debugf(ctx, "[Stage 1a] Finished listing all desired resources")
	return nil
}

// stageListActual lists resources from the configured platform provider for all configured kinds.
// It uses an intermediate channel and goroutine to avoid blocking the provider on downstream processing.
func (e *DriftAnalysisEngine) stageListActual(ctx context.Context, actualChan chan<- domain.PlatformResource) error {
	defer close(actualChan)                                         // Ensure output channel is closed eventually
	platformResourceChan := make(chan domain.PlatformResource, 100) // Intermediate channel
	var wg sync.WaitGroup
	wg.Add(1)

	// Goroutine to forward resources from the intermediate channel to the main output channel
	go func() {
		defer wg.Done()
		for res := range platformResourceChan {
			select {
			case actualChan <- res:
			case <-ctx.Done():
				e.logger.Warnf(ctx, "[Stage 1b] Context cancelled while forwarding platform resource")
				return
			}
		}
	}()

	e.logger.Debugf(ctx, "[Stage 1b] Initiating listing of actual resources")
	platformFilters := make(map[string]string) // Placeholder, filters loaded from config if needed by provider
	// Call the platform provider's ListResources method. This blocks until the provider is done listing.
	err := e.platformProvider.ListResources(ctx, e.runConfig.ResourceKindsToProcess, platformFilters, platformResourceChan)
	// Close the intermediate channel *after* the provider finishes or errors out
	close(platformResourceChan)
	// Wait for the forwarding goroutine to finish processing all items from the intermediate channel
	wg.Wait()

	if err != nil {
		// Provider already logged specifics, just log stage failure and return
		e.logger.Errorf(ctx, err, "[Stage 1b] Platform provider ListResources failed")
		return err // Propagate provider error
	}
	e.logger.Debugf(ctx, "[Stage 1b] Finished listing all actual resources")
	return nil
}

// stageMatchResources collects all listed resources and passes them to the matcher.
func (e *DriftAnalysisEngine) stageMatchResources(
	ctx context.Context,
	desiredChan <-chan domain.StateResource,
	actualChan <-chan domain.PlatformResource,
	matchResultChan chan<- ports.MatchingResult,
) error {
	defer close(matchResultChan) // Ensure channel is closed
	e.logger.Debugf(ctx, "[Stage 2] Waiting to collect resources from listing stages...")
	// Collect all results from the input channels first
	desired, actual, err := e.collectResources(ctx, desiredChan, actualChan) // Uses helper
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		} // Context cancelled during collection
		e.logger.Errorf(ctx, err, "[Stage 2] Failed collecting resources for matching")
		return errors.Wrap(err, errors.CodeInternal, "failed collecting resources for matching")
	}
	// Check context again after collection finishes, before matching
	if ctx.Err() != nil {
		return ctx.Err()
	}

	e.logger.Debugf(ctx, "[Stage 2] Starting resource matching")
	matchResult, err := e.matcher.Match(ctx, desired, actual)
	if err != nil {
		e.logger.Errorf(ctx, err, "[Stage 2] Resource matching failed")
		return errors.Wrap(err, errors.CodeMatchingError, "resource matching failed")
	}
	e.logger.Debugf(ctx, "[Stage 2] Matching complete: %d matched, %d missing, %d unmanaged", len(matchResult.Matched), len(matchResult.UnmatchedDesired), len(matchResult.UnmatchedActual))

	// Send the single matching result object
	select {
	case matchResultChan <- matchResult:
		e.logger.Debugf(ctx, "[Stage 2] Match results sent")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stageDispatchComparisons processes the matching results, handles unmatched resources,
// and sends matched pairs to the comparison workers.
func (e *DriftAnalysisEngine) stageDispatchComparisons(
	ctx context.Context,
	matchResultChan <-chan ports.MatchingResult,
	compareInputChan chan<- ports.MatchedPair,
	finalResults *[]domain.ComparisonResult,
	finalResultsMutex *sync.Mutex,
) error {
	defer close(compareInputChan) // Ensure comparison input channel is closed
	e.logger.Debugf(ctx, "[Stage 3] Waiting for match results...")
	select {
	case matchResult, ok := <-matchResultChan:
		if !ok { // Channel closed without sending a result (e.g., matcher errored)
			if ctx.Err() == nil {
				e.logger.Warnf(ctx, "[Stage 3] Matcher did not produce a result, compare stage potentially skipped")
			}
			return nil // Not an error for this stage if context is okay
		}
		e.logger.Debugf(ctx, "[Stage 3] Received match results, processing unmatched...")
		// Process resources found only in state or only on platform
		e.processUnmatched(ctx, matchResult, finalResults, finalResultsMutex)

		e.logger.Debugf(ctx, "[Stage 3] Dispatching %d matched pairs for comparison...", len(matchResult.Matched))
		// Send matched pairs to the comparison workers
		for _, pair := range matchResult.Matched {
			select {
			case compareInputChan <- pair:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		e.logger.Debugf(ctx, "[Stage 3] Finished dispatching matched pairs")
		return nil
	case <-ctx.Done():
		return ctx.Err() // Context cancelled while waiting for match result
	}
}

// stageCompareResources manages a pool of workers to perform comparisons concurrently.
func (e *DriftAnalysisEngine) stageCompareResources(
	ctx context.Context,
	compareInputChan <-chan ports.MatchedPair,
	comparisonResultChan chan<- domain.ComparisonResult,
) error {
	defer close(comparisonResultChan) // Ensure result channel is closed when all workers finish
	var compareWG sync.WaitGroup
	e.logger.Debugf(ctx, "[Stage 4] Starting %d comparison workers...", e.runConfig.Concurrency)

	// Launch worker goroutines up to the configured concurrency limit
	for i := 0; i < e.runConfig.Concurrency; i++ {
		compareWG.Add(1)
		go func(workerID int) {
			defer compareWG.Done()
			workerLogger := e.logger.WithFields(map[string]any{"worker_id": workerID})
			// Pass the map of attributes to check for all kinds down to the worker
			e.compareWorker(ctx, compareInputChan, comparisonResultChan, e.runConfig.AttributesToCheck, workerLogger)
		}(i)
	}
	compareWG.Wait() // Wait for all workers to drain the input channel and finish
	e.logger.Debugf(ctx, "[Stage 4] All comparison workers finished")
	return nil
}

// stageAggregateResults collects all individual comparison results into the final slice.
func (e *DriftAnalysisEngine) stageAggregateResults(
	ctx context.Context,
	comparisonResultChan <-chan domain.ComparisonResult,
	finalResults *[]domain.ComparisonResult,
	finalResultsMutex *sync.Mutex,
) error {
	e.logger.Debugf(ctx, "[Stage 5] Starting result aggregation...")
	count := 0
	for result := range comparisonResultChan {
		// Check context frequently in case comparison stage takes a long time
		if ctx.Err() != nil {
			e.logger.Warnf(ctx, "[Stage 5] Context cancelled during aggregation")
			return ctx.Err()
		}
		// Append result to shared slice under mutex protection
		finalResultsMutex.Lock()
		*finalResults = append(*finalResults, result)
		count++
		finalResultsMutex.Unlock()
	}
	e.logger.Debugf(ctx, "[Stage 5] Finished aggregating %d comparison results", count)
	return nil
}

// reportResults calls the configured reporter to output the final results.
func (e *DriftAnalysisEngine) reportResults(ctx context.Context, results []domain.ComparisonResult) error {
	e.logger.Infof(ctx, "[Stage 6] Reporting %d results...", len(results))
	reportErr := e.reporter.Report(ctx, results)
	if reportErr != nil {
		e.logger.Errorf(ctx, reportErr, "[Stage 6] Failed to generate final report")
		return errors.Wrap(reportErr, errors.CodeInternal, "failed to generate final report")
	}
	e.logger.Infof(ctx, "[Stage 6] Reporting complete.")
	return nil
}

// --- Worker and Helper Functions ---

// compareWorker processes comparison tasks read from the input channel.
func (e *DriftAnalysisEngine) compareWorker(
	ctx context.Context,
	inputChan <-chan ports.MatchedPair,
	resultChan chan<- domain.ComparisonResult,
	attributesToCheckMap map[domain.ResourceKind][]string,
	logger ports.Logger,
) {
	logger.Debugf(ctx, "Comparison worker started")
	for pair := range inputChan {
		// Check context before processing each pair
		select {
		case <-ctx.Done():
			logger.Warnf(ctx, "Comparison worker shutting down due to context cancellation")
			return
		default:
			e.processSingleComparison(ctx, pair, attributesToCheckMap, resultChan, logger)
		}
	}
	logger.Debugf(ctx, "Comparison worker finished")
}

// processSingleComparison performs the comparison for one resource pair.
func (e *DriftAnalysisEngine) processSingleComparison(
	ctx context.Context,
	pair ports.MatchedPair,
	attributesToCheckMap map[domain.ResourceKind][]string,
	resultChan chan<- domain.ComparisonResult,
	logger ports.Logger,
) {
	desiredMeta := pair.Desired.Metadata()
	actualMeta := pair.Actual.Metadata()
	kind := desiredMeta.Kind // Assume Kind matches as they were paired

	log := logger.WithFields(map[string]any{
		"resource_kind": kind,
		"resource_id":   actualMeta.ProviderAssignedID,
		"source_id":     desiredMeta.SourceIdentifier,
	})

	log.Debugf(ctx, "Comparing resource pair")

	comparer, err := e.getComparerForKind(ctx, kind, log)
	if err != nil {
		e.sendComparisonError(ctx, kind, desiredMeta, actualMeta, err, resultChan, log)
		return
	}

	attributesForThisKind := attributesToCheckMap[kind]
	if attributesForThisKind == nil {
		log.Warnf(ctx, "No attributes configured for comparison for kind %s, skipping", kind)
		result := domain.ComparisonResult{
			Status:             domain.StatusNoDrift,
			ResourceKind:       kind,
			SourceIdentifier:   desiredMeta.SourceIdentifier,
			ProviderType:       actualMeta.ProviderType,
			ProviderAssignedID: actualMeta.ProviderAssignedID,
		}
		e.sendResult(ctx, result, resultChan, log)
		return
	}

	log.Debugf(ctx, "Comparing attributes: %v", attributesForThisKind)
	diffs, cmpErr := comparer.Compare(ctx, pair.Desired, pair.Actual, attributesForThisKind)

	result := e.createComparisonResult(kind, desiredMeta, actualMeta, diffs, cmpErr, log)
	e.sendResult(ctx, result, resultChan, log)
}

// getComparerForKind retrieves the specific comparer implementation from the registry.
func (e *DriftAnalysisEngine) getComparerForKind(ctx context.Context, kind domain.ResourceKind, logger ports.Logger) (ports.ResourceComparer, error) {
	comparer, err := e.registry.GetResourceComparer(kind)
	if err != nil {
		logger.Errorf(ctx, err, "Failed to get comparer")
		return nil, err
	}
	return comparer, nil
}

// createComparisonResult determines the status based on comparison outcome.
func (e *DriftAnalysisEngine) createComparisonResult(
	kind domain.ResourceKind,
	desiredMeta domain.ResourceMetadata,
	actualMeta domain.ResourceMetadata,
	diffs []domain.AttributeDiff,
	cmpErr error,
	logger ports.Logger,
) domain.ComparisonResult {
	result := domain.ComparisonResult{
		ResourceKind:       kind,
		SourceIdentifier:   desiredMeta.SourceIdentifier,
		ProviderType:       actualMeta.ProviderType,
		ProviderAssignedID: actualMeta.ProviderAssignedID,
		Differences:        diffs,
		Error:              cmpErr,
	}

	if cmpErr != nil {
		result.Status = domain.StatusError
		logger.Errorf(nil, cmpErr, "Comparison failed")
	} else if len(diffs) > 0 {
		result.Status = domain.StatusDrifted
		logger.Warnf(nil, "Drift detected")
	} else {
		result.Status = domain.StatusNoDrift
		logger.Debugf(nil, "No drift detected")
	}
	return result
}

// sendComparisonError is a helper to create and send an error result.
func (e *DriftAnalysisEngine) sendComparisonError(
	ctx context.Context,
	kind domain.ResourceKind,
	desiredMeta domain.ResourceMetadata,
	actualMeta domain.ResourceMetadata,
	err error,
	resultChan chan<- domain.ComparisonResult,
	logger ports.Logger,
) {
	result := domain.ComparisonResult{
		Status:             domain.StatusError,
		ResourceKind:       kind,
		SourceIdentifier:   desiredMeta.SourceIdentifier,
		ProviderType:       actualMeta.ProviderType,
		ProviderAssignedID: actualMeta.ProviderAssignedID,
		Error:              err,
	}
	e.sendResult(ctx, result, resultChan, logger)
}

// sendResult sends a ComparisonResult to the channel, handling context cancellation.
func (e *DriftAnalysisEngine) sendResult(
	ctx context.Context,
	result domain.ComparisonResult,
	resultChan chan<- domain.ComparisonResult,
	logger ports.Logger,
) {
	select {
	case resultChan <- result:
	case <-ctx.Done():
		logger.Warnf(ctx, "Context cancelled before sending comparison result for %s", result.SourceIdentifier)
	}
}

// collectResources collects all resources from the desired and actual channels.
// It uses named helper goroutines (drainStateChannel, drainPlatformChannel).
func (e *DriftAnalysisEngine) collectResources(ctx context.Context,
	desiredChan <-chan domain.StateResource,
	actualChan <-chan domain.PlatformResource,
) ([]domain.StateResource, []domain.PlatformResource, error) {

	var desired []domain.StateResource
	var actual []domain.PlatformResource
	var wg sync.WaitGroup
	collectDone := make(chan struct{})
	errChannel := make(chan error, 2) // Buffered channel for potential context errors

	wg.Add(2)
	go e.drainStateChannel(ctx, desiredChan, &desired, &wg, errChannel)
	go e.drainPlatformChannel(ctx, actualChan, &actual, &wg, errChannel)

	go func() {
		wg.Wait()
		close(collectDone)
		close(errChannel)
	}()

	select {
	case <-collectDone:
		if err := <-errChannel; err != nil {
			return nil, nil, err // Return first error (likely context cancelled)
		}
		if ctx.Err() != nil { // Double check context after wait
			return nil, nil, ctx.Err()
		}
		e.logger.Debugf(ctx, "Collected %d desired and %d actual resources", len(desired), len(actual))
		return desired, actual, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// drainStateChannel reads desired resources from the input channel into the output slice.
// It signals errors (like context cancellation) via the errChan.
func (e *DriftAnalysisEngine) drainStateChannel(ctx context.Context, in <-chan domain.StateResource, out *[]domain.StateResource, wg *sync.WaitGroup, errChan chan<- error) {
	defer wg.Done()
	for {
		select {
		case res, ok := <-in:
			if !ok {
				return // Channel closed
			}
			*out = append(*out, res)
		case <-ctx.Done():
			select {
			case errChan <- ctx.Err():
			default:
			} // Signal context error non-blockingly
			return // Stop processing
		}
	}
}

// drainPlatformChannel reads actual resources from the input channel into the output slice.
// It signals errors (like context cancellation) via the errChan.
func (e *DriftAnalysisEngine) drainPlatformChannel(ctx context.Context, in <-chan domain.PlatformResource, out *[]domain.PlatformResource, wg *sync.WaitGroup, errChan chan<- error) {
	defer wg.Done()
	for {
		select {
		case res, ok := <-in:
			if !ok {
				return // Channel closed
			}
			*out = append(*out, res)
		case <-ctx.Done():
			select {
			case errChan <- ctx.Err():
			default:
			} // Signal context error non-blockingly
			return // Stop processing
		}
	}
}

// processUnmatched creates ComparisonResult entries for unmatched resources.
func (e *DriftAnalysisEngine) processUnmatched(ctx context.Context, matchResult ports.MatchingResult, finalResults *[]domain.ComparisonResult, mutex *sync.Mutex) {
	mutex.Lock()
	defer mutex.Unlock()

	for _, res := range matchResult.UnmatchedDesired {
		meta := res.Metadata()
		*finalResults = append(*finalResults, domain.ComparisonResult{
			Status:           domain.StatusMissing,
			ResourceKind:     meta.Kind,
			SourceIdentifier: meta.SourceIdentifier,
			ProviderType:     meta.ProviderType, // From state source
		})
		e.logger.Warnf(ctx, "Resource missing on platform: [%s] %s", meta.Kind, meta.SourceIdentifier)
	}

	for _, res := range matchResult.UnmatchedActual {
		meta := res.Metadata()
		*finalResults = append(*finalResults, domain.ComparisonResult{
			Status:             domain.StatusUnmanaged,
			ResourceKind:       meta.Kind,
			ProviderType:       meta.ProviderType, // From platform
			ProviderAssignedID: meta.ProviderAssignedID,
		})
		e.logger.Warnf(ctx, "Unmanaged resource found on platform: [%s] %s", meta.Kind, meta.ProviderAssignedID)
	}
}
