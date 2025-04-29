package text

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

const ReporterTypeText = "text"

type Config struct {
	NoColor bool `yaml:"no_color"`
}

type Reporter struct {
	config Config
	writer io.Writer
	logger ports.Logger
}

func NewReporter(cfg Config, logger ports.Logger) (*Reporter, error) {
	if cfg.NoColor || !isTerminal(os.Stdout) {
		color.NoColor = true
	}

	return &Reporter{
		config: cfg,
		writer: os.Stdout,
		logger: logger,
	}, nil
}

func isTerminal(f *os.File) bool {
	stat, _ := f.Stat()
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func (r *Reporter) Report(ctx context.Context, results []domain.ComparisonResult) error {
	if len(results) == 0 {
		fmt.Fprintln(r.writer, "No resources found or processed.")
		return nil
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].ResourceKind != results[j].ResourceKind {
			return results[i].ResourceKind < results[j].ResourceKind
		}
		idI := results[i].SourceIdentifier
		if idI == "" {
			idI = results[i].ProviderAssignedID
		}
		idJ := results[j].SourceIdentifier
		if idJ == "" {
			idJ = results[j].ProviderAssignedID
		}
		return idI < idJ
	})

	tw := tabwriter.NewWriter(r.writer, 0, 8, 2, ' ', 0)
	defer tw.Flush()

	red := color.New(color.FgRed).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	magenta := color.New(color.FgMagenta).SprintFunc()

	fmt.Fprintln(tw, "Drift Analysis Report")
	fmt.Fprintln(tw, "=====================")
	fmt.Fprintln(tw, "Status\tKind\tIdentifier\tDetails")
	fmt.Fprintln(tw, "------\t----\t----------\t-------")

	driftCount := 0
	errorCount := 0
	missingCount := 0
	unmanagedCount := 0
	noDriftCount := 0

	for _, res := range results {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		identifier := res.SourceIdentifier
		if identifier == "" {
			identifier = res.ProviderAssignedID
		}
		if identifier == "" {
			identifier = "<unknown>"
		}

		statusStr := ""
		details := ""

		switch res.Status {
		case domain.StatusDrifted:
			driftCount++
			statusStr = red("[DRIFT]")
			details = r.formatDriftDetails(res.Differences)
		case domain.StatusError:
			errorCount++
			statusStr = magenta("[ERROR]")
			details = fmt.Sprintf("Comparison failed: %v", res.Error)
			if appErr := (*apperrors.AppError)(nil); errors.As(res.Error, &appErr) {
				if appErr.IsUserFacing {
					details += fmt.Sprintf(" (%s)", appErr.Message)
				}
			}
		case domain.StatusMissing:
			missingCount++
			statusStr = yellow("[MISSING]")
			details = "Resource defined in state but not found on platform."
		case domain.StatusUnmanaged:
			unmanagedCount++
			statusStr = cyan("[UNMANAGED]")
			details = "Resource found on platform but not defined in state."
			identifier = res.ProviderAssignedID
		case domain.StatusNoDrift:
			noDriftCount++
			statusStr = green("[OK]")
			details = "No drift detected."
		default:
			statusStr = "[UNKNOWN]"
			details = "Unknown comparison status."
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", statusStr, res.ResourceKind, identifier, details)
	}

	fmt.Fprintln(tw, "\nSummary:")
	fmt.Fprintln(tw, "-------")
	fmt.Fprintf(tw, "Total Resources Processed:\t%d\n", len(results))
	fmt.Fprintf(tw, "No Drift:\t%s\n", green(noDriftCount))
	fmt.Fprintf(tw, "Drifted:\t%s\n", red(driftCount))
	fmt.Fprintf(tw, "Missing (in State only):\t%s\n", yellow(missingCount))
	fmt.Fprintf(tw, "Unmanaged (on Platform only):\t%s\n", cyan(unmanagedCount))
	fmt.Fprintf(tw, "Errors:\t%s\n", magenta(errorCount))

	return nil
}

func (r *Reporter) formatDriftDetails(diffs []domain.AttributeDiff) string {
	if len(diffs) == 0 {
		return "Drift detected but no specific differences recorded."
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("%d attributes differ: ", len(diffs)))
	for i, diff := range diffs {
		if i > 0 {
			builder.WriteString("; ")
		}
		builder.WriteString(fmt.Sprintf("%s=[Expected: %v, Actual: %v]",
			diff.AttributeName,
			r.formatValue(diff.ExpectedValue),
			r.formatValue(diff.ActualValue)))
		if diff.Details != "" {
			builder.WriteString(fmt.Sprintf(" (%s)", diff.Details))
		}
	}
	return builder.String()
}

func (r *Reporter) formatValue(value any) string {
	const maxLen = 100
	str := fmt.Sprintf("%v", value)
	if len(str) > maxLen {
		return str[:maxLen-3] + "..."
	}
	return str
}
