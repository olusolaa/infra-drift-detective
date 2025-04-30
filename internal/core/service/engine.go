package service

import (
	"context"
	"sync"

	"github.com/olusolaa/infra-drift-detector/internal/config"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"golang.org/x/sync/errgroup"
)

type DriftAnalysisEngine struct {
	registry         *ComponentRegistry
	matcher          ports.Matcher
	reporter         ports.Reporter
	logger           ports.Logger
	appConfig        *config.Config
	concurrency      int
	stateProvider    ports.StateProvider
	platformProvider ports.PlatformProvider
}

func NewDriftAnalysisEngine(
	registry *ComponentRegistry,
	matcher ports.Matcher,
	reporter ports.Reporter,
	logger ports.Logger,
	appConfig *config.Config,
	concurrency int,
	stateProvider ports.StateProvider,
	platformProvider ports.PlatformProvider,
) (*DriftAnalysisEngine, error) {
	if concurrency <= 0 {
		concurrency = 10
	}
	if stateProvider == nil {
		return nil, errors.New(errors.CodeConfigValidation, "state provider cannot be nil")
	}
	if platformProvider == nil {
		return nil, errors.New(errors.CodeConfigValidation, "platform provider cannot be nil")
	}

	return &DriftAnalysisEngine{
		registry:         registry,
		matcher:          matcher,
		reporter:         reporter,
		logger:           logger,
		appConfig:        appConfig,
		concurrency:      concurrency,
		stateProvider:    stateProvider,
		platformProvider: platformProvider,
	}, nil
}

//consider breaking the run into multiple functions
// add comment to help explain the flow

func (e *DriftAnalysisEngine) Run(ctx context.Context) error {
	e.logger.Infof(ctx, "Starting drift analysis run using %s state and %s platform providers",
		e.stateProvider.Type(), e.platformProvider.Type())

	resourceKindsToProcess := e.appConfig.GetResourceKinds()
	if len(resourceKindsToProcess) == 0 {
		return errors.NewUserFacing(errors.CodeConfigValidation, "no resource kinds configured for analysis", "Please define resources in the configuration file.")
	}
	e.logger.Debugf(ctx, "Planning to process kinds: %v", resourceKindsToProcess)

	desiredChan := make(chan domain.StateResource, 100)
	actualChan := make(chan domain.PlatformResource, 100)
	matchResultChan := make(chan ports.MatchingResult, 1)
	compareInputChan := make(chan ports.MatchedPair, 100)
	comparisonResultChan := make(chan domain.ComparisonResult, 100)

	g, childCtx := errgroup.WithContext(ctx)
	var finalResults []domain.ComparisonResult
	var finalResultsMutex sync.Mutex

	g.Go(func() error {
		defer close(desiredChan)
		for _, kind := range resourceKindsToProcess {
			if childCtx.Err() != nil {
				return childCtx.Err()
			}
			e.logger.Debugf(childCtx, "Listing desired resources of kind: %s", kind)
			resources, err := e.stateProvider.ListResources(childCtx, kind)
			if err != nil {
				wrappedErr := errors.Wrap(err, errors.CodeStateReadError, "failed listing desired resources")
				e.logger.Errorf(childCtx, wrappedErr, "error listing desired kind %s", kind)
				return wrappedErr
			}
			e.logger.Debugf(childCtx, "Found %d desired resources of kind: %s", len(resources), kind)
			for _, res := range resources {
				select {
				case desiredChan <- res:
				case <-childCtx.Done():
					return childCtx.Err()
				}
			}
		}
		e.logger.Debugf(childCtx, "Finished listing all desired resources")
		return nil
	})

	g.Go(func() error {
		defer close(actualChan)
		platformResourceChan := make(chan domain.PlatformResource, 100)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for res := range platformResourceChan {
				select {
				case actualChan <- res:
				case <-childCtx.Done():
					e.logger.Warnf(childCtx, "Context cancelled while forwarding platform resource")
					return
				}
			}
		}()

		platformFilters := make(map[string]string)

		err := e.platformProvider.ListResources(childCtx, resourceKindsToProcess, platformFilters, platformResourceChan)
		close(platformResourceChan)
		wg.Wait()

		if err != nil {
			return err
		}

		e.logger.Debugf(childCtx, "Finished listing all actual resources")
		return nil
	})

	g.Go(func() error {
		defer close(matchResultChan)
		desired, actual, err := e.collectResources(childCtx, desiredChan, actualChan)
		if err != nil {
			if childCtx.Err() != nil {
				return childCtx.Err()
			}
			return errors.Wrap(err, errors.CodeInternal, "failed collecting resources for matching")
		}
		if childCtx.Err() != nil {
			return childCtx.Err()
		}

		e.logger.Debugf(childCtx, "Starting resource matching")
		matchResult, err := e.matcher.Match(childCtx, desired, actual)
		if err != nil {
			return errors.Wrap(err, errors.CodeMatchingError, "resource matching failed")
		}
		e.logger.Debugf(childCtx, "Matching complete: %d matched, %d missing, %d unmanaged", len(matchResult.Matched), len(matchResult.UnmatchedDesired), len(matchResult.UnmatchedActual))

		select {
		case matchResultChan <- matchResult:
			return nil
		case <-childCtx.Done():
			return childCtx.Err()
		}
	})

	g.Go(func() error {
		defer close(compareInputChan)
		select {
		case matchResult, ok := <-matchResultChan:
			if !ok {
				if childCtx.Err() == nil {
					e.logger.Warnf(childCtx, "Matcher did not produce a result, compare stage skipped")
				}
				return nil
			}
			e.processUnmatched(childCtx, matchResult, &finalResults, &finalResultsMutex)
			for _, pair := range matchResult.Matched {
				select {
				case compareInputChan <- pair:
				case <-childCtx.Done():
					return childCtx.Err()
				}
			}
			e.logger.Debugf(childCtx, "Dispatched %d matched pairs for comparison", len(matchResult.Matched))
			return nil
		case <-childCtx.Done():
			return childCtx.Err()
		}
	})

	var compareWG sync.WaitGroup
	g.Go(func() error {
		defer close(comparisonResultChan)
		for i := 0; i < e.concurrency; i++ {
			compareWG.Add(1)
			go func() {
				defer compareWG.Done()
				e.compareWorker(childCtx, compareInputChan, comparisonResultChan)
			}()
		}
		compareWG.Wait()
		e.logger.Debugf(childCtx, "All comparison workers finished")
		return nil
	})

	g.Go(func() error {
		for result := range comparisonResultChan {
			if childCtx.Err() != nil {
				return childCtx.Err()
			}
			finalResultsMutex.Lock()
			finalResults = append(finalResults, result)
			finalResultsMutex.Unlock()
		}
		e.logger.Debugf(childCtx, "Finished aggregating comparison results")
		return nil
	})

	if runErr := g.Wait(); runErr != nil {
		if runErr == context.Canceled || runErr == context.DeadlineExceeded {
			e.logger.Warnf(ctx, "Drift analysis workflow cancelled or timed out: %v", runErr)
		} else {
			e.logger.Errorf(ctx, runErr, "drift analysis workflow encountered an error")
		}

		if len(finalResults) > 0 && runErr != context.Canceled && runErr != context.DeadlineExceeded {
			reportErr := e.reporter.Report(ctx, finalResults)
			if reportErr != nil {
				e.logger.Errorf(ctx, reportErr, "failed to report partial results after error")
			}
		}
		return runErr
	}

	e.logger.Infof(ctx, "Drift analysis workflow completed, reporting %d results", len(finalResults))
	reportErr := e.reporter.Report(ctx, finalResults)
	if reportErr != nil {
		return errors.Wrap(reportErr, errors.CodeInternal, "failed to generate final report")
	}

	e.logger.Infof(ctx, "Drift analysis run finished successfully")
	return nil
}

func (e *DriftAnalysisEngine) collectResources(ctx context.Context,
	desiredChan <-chan domain.StateResource,
	actualChan <-chan domain.PlatformResource,
) ([]domain.StateResource, []domain.PlatformResource, error) {

	var desired []domain.StateResource
	var actual []domain.PlatformResource
	var wg sync.WaitGroup
	collectDone := make(chan struct{})

	wg.Add(2)
	go func() {
		// can this be it own function
		defer wg.Done()
		for {
			select {
			case res, ok := <-desiredChan:
				if !ok {
					return
				}
				desired = append(desired, res)
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		// can this be it own function
		defer wg.Done()
		for {
			select {
			case res, ok := <-actualChan:
				if !ok {
					return
				}
				actual = append(actual, res)
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(collectDone)
	}()

	select {
	case <-collectDone:
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		e.logger.Debugf(ctx, "Collected %d desired and %d actual resources", len(desired), len(actual))
		return desired, actual, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func (e *DriftAnalysisEngine) processUnmatched(ctx context.Context, matchResult ports.MatchingResult, finalResults *[]domain.ComparisonResult, mutex *sync.Mutex) {
	mutex.Lock()
	defer mutex.Unlock()

	for _, res := range matchResult.UnmatchedDesired {
		meta := res.Metadata()
		*finalResults = append(*finalResults, domain.ComparisonResult{
			Status:           domain.StatusMissing,
			ResourceKind:     meta.Kind,
			SourceIdentifier: meta.SourceIdentifier,
			ProviderType:     meta.ProviderType,
		})
		e.logger.Warnf(ctx, "Resource missing on platform: [%s] %s", meta.Kind, meta.SourceIdentifier)
	}

	for _, res := range matchResult.UnmatchedActual {
		meta := res.Metadata()
		*finalResults = append(*finalResults, domain.ComparisonResult{
			Status:             domain.StatusUnmanaged,
			ResourceKind:       meta.Kind,
			ProviderType:       meta.ProviderType,
			ProviderAssignedID: meta.ProviderAssignedID,
		})
		e.logger.Warnf(ctx, "Unmanaged resource found on platform: [%s] %s", meta.Kind, meta.ProviderAssignedID)
	}
}

// can this be broken into multiple functions
func (e *DriftAnalysisEngine) compareWorker(ctx context.Context,
	inputChan <-chan ports.MatchedPair,
	resultChan chan<- domain.ComparisonResult,
) {
	for pair := range inputChan {
		select {
		case <-ctx.Done():
			return
		default:
		}

		desiredMeta := pair.Desired.Metadata()
		actualMeta := pair.Actual.Metadata()
		kind := desiredMeta.Kind

		log := e.logger.WithFields(map[string]any{
			"resource_kind": kind,
			"resource_id":   actualMeta.ProviderAssignedID,
			"source_id":     desiredMeta.SourceIdentifier,
		})

		log.Debugf(ctx, "Comparing resource pair")

		comparer, err := e.registry.GetResourceComparer(kind)
		if err != nil {
			log.Errorf(ctx, err, "Failed to get comparer for kind")
			result := domain.ComparisonResult{
				Status:             domain.StatusError,
				ResourceKind:       kind,
				SourceIdentifier:   desiredMeta.SourceIdentifier,
				ProviderType:       actualMeta.ProviderType,
				ProviderAssignedID: actualMeta.ProviderAssignedID,
				Error:              err,
			}
			select {
			case resultChan <- result:
			case <-ctx.Done():
				log.Warnf(ctx, "Context cancelled before sending comparer error result")
				return
			}
			continue
		}

		attributesToCheck := e.appConfig.GetAttributesForKind(kind)
		if attributesToCheck == nil {
			log.Warnf(ctx, "No attributes configured for comparison for kind %s, skipping comparison", kind)
			result := domain.ComparisonResult{
				Status:             domain.StatusNoDrift,
				ResourceKind:       kind,
				SourceIdentifier:   desiredMeta.SourceIdentifier,
				ProviderType:       actualMeta.ProviderType,
				ProviderAssignedID: actualMeta.ProviderAssignedID,
			}
			select {
			case resultChan <- result:
			case <-ctx.Done():
				log.Warnf(ctx, "Context cancelled before sending no-attribute result")
				return
			}
			continue
		}

		log.Debugf(ctx, "Comparing attributes: %v", attributesToCheck)
		diffs, cmpErr := comparer.Compare(ctx, pair.Desired, pair.Actual, attributesToCheck)

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
			log.Errorf(ctx, cmpErr, "Comparison failed")
		} else if len(diffs) > 0 {
			result.Status = domain.StatusDrifted
			log.Warnf(ctx, "Drift detected")
		} else {
			result.Status = domain.StatusNoDrift
			log.Debugf(ctx, "No drift detected")
		}

		select {
		case resultChan <- result:
		case <-ctx.Done():
			log.Warnf(ctx, "Context cancelled before sending comparison result")
			return
		}
	}
}
