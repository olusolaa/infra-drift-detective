// --- START OF FILE infra-drift-detector/internal/reporting/json/reporter.go ---
package json

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
)

const ReporterTypeJSON = "json"

type Config struct {
	// PrettyPrint bool `yaml:"pretty_print"` // Future option for non-indented JSON
}

type Reporter struct {
	config Config
	writer io.Writer
	logger ports.Logger
}

func NewReporter(cfg Config, logger ports.Logger) (*Reporter, error) {
	return &Reporter{
		config: cfg,
		writer: os.Stdout,
		logger: logger,
	}, nil
}

type jsonReport struct {
	Summary jsonSummary      `json:"summary"`
	Results []jsonResultItem `json:"results"`
}

type jsonSummary struct {
	TotalResourcesProcessed int `json:"total_resources_processed"`
	NoDrift                 int `json:"no_drift"`
	Drifted                 int `json:"drifted"`
	Missing                 int `json:"missing"`
	Unmanaged               int `json:"unmanaged"`
	Errors                  int `json:"errors"`
}

type jsonResultItem struct {
	Status             domain.ComparisonStatus `json:"status"`
	ResourceKind       domain.ResourceKind     `json:"resource_kind"`
	SourceIdentifier   string                  `json:"source_identifier,omitempty"`
	ProviderType       string                  `json:"provider_type,omitempty"`
	ProviderAssignedID string                  `json:"provider_assigned_id,omitempty"`
	Differences        []jsonAttributeDiff     `json:"differences,omitempty"`
	ErrorMessage       string                  `json:"error_message,omitempty"`
}

type jsonAttributeDiff struct {
	AttributeName string `json:"attribute_name"`
	ExpectedValue any    `json:"expected_value"`
	ActualValue   any    `json:"actual_value"`
	Details       string `json:"details,omitempty"`
}

func (r *Reporter) Report(ctx context.Context, results []domain.ComparisonResult) error {
	report := jsonReport{
		Summary: jsonSummary{TotalResourcesProcessed: len(results)},
		Results: make([]jsonResultItem, 0, len(results)),
	}

	for _, res := range results {
		if ctx.Err() != nil {
			r.logger.Warnf(ctx, "JSON report generation cancelled.")
			return ctx.Err()
		}

		switch res.Status {
		case domain.StatusNoDrift:
			report.Summary.NoDrift++
		case domain.StatusDrifted:
			report.Summary.Drifted++
		case domain.StatusMissing:
			report.Summary.Missing++
		case domain.StatusUnmanaged:
			report.Summary.Unmanaged++
		case domain.StatusError:
			report.Summary.Errors++
		}

		item := jsonResultItem{
			Status:             res.Status,
			ResourceKind:       res.ResourceKind,
			SourceIdentifier:   res.SourceIdentifier,
			ProviderType:       res.ProviderType,
			ProviderAssignedID: res.ProviderAssignedID,
		}

		if res.Error != nil {
			item.ErrorMessage = res.Error.Error()
		}

		if len(res.Differences) > 0 {
			item.Differences = make([]jsonAttributeDiff, len(res.Differences))
			for i, diff := range res.Differences {
				item.Differences[i] = jsonAttributeDiff{
					AttributeName: diff.AttributeName,
					ExpectedValue: diff.ExpectedValue,
					ActualValue:   diff.ActualValue,
					Details:       diff.Details,
				}
			}
		}

		if item.SourceIdentifier == "" {
			item.SourceIdentifier = ""
		}
		if item.ProviderType == "" {
			item.ProviderType = ""
		}
		if item.ProviderAssignedID == "" {
			item.ProviderAssignedID = ""
		}

		report.Results = append(report.Results, item)
	}

	encoder := json.NewEncoder(r.writer)
	encoder.SetIndent("", "  ")

	err := encoder.Encode(report)
	if err != nil {
		r.logger.Errorf(ctx, err, "Failed to encode JSON report")
		fmt.Fprintf(r.writer, `{"error": "failed to generate JSON report: %v"}\n`, err)
		return fmt.Errorf("failed to encode JSON report: %w", err)
	}

	r.logger.Debugf(ctx, "JSON report successfully generated.")
	return nil
}
