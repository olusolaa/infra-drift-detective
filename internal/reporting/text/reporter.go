package text

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fatih/color"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/pmezard/go-difflib/difflib"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"text/tabwriter"
)

const ReporterTypeText = "text"

type Config struct {
	NoColor bool `yaml:"no_color"`
}

type Reporter struct {
	config Config
	writer io.Writer
	logger ports.Logger

	red     func(...interface{}) string
	yellow  func(...interface{}) string
	green   func(...interface{}) string
	cyan    func(...interface{}) string
	magenta func(...interface{}) string
	bold    func(...interface{}) string
	diffAdd func(...interface{}) string
	diffDel func(...interface{}) string
}

func NewReporter(cfg Config, logger ports.Logger) (*Reporter, error) {
	if cfg.NoColor || !isTerminal(os.Stdout) {
		color.NoColor = true
	}

	return &Reporter{
		config:  cfg,
		writer:  os.Stdout,
		logger:  logger,
		red:     color.New(color.FgRed).SprintFunc(),
		yellow:  color.New(color.FgYellow).SprintFunc(),
		green:   color.New(color.FgGreen).SprintFunc(),
		cyan:    color.New(color.FgCyan).SprintFunc(),
		magenta: color.New(color.FgMagenta).SprintFunc(),
		bold:    color.New(color.Bold).SprintFunc(),
		diffAdd: color.New(color.FgGreen).SprintFunc(),
		diffDel: color.New(color.FgRed).SprintFunc(),
	}, nil
}

func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func (r *Reporter) Report(ctx context.Context, results []domain.ComparisonResult) error {
	if len(results) == 0 {
		fmt.Fprintln(r.writer, r.yellow("No resources found or processed."))
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
		if idI == "" && idJ != "" {
			return false
		}
		if idI != "" && idJ == "" {
			return true
		}
		return idI < idJ
	})

	tw := tabwriter.NewWriter(r.writer, 0, 8, 2, ' ', 0)

	fmt.Fprintln(tw, r.bold("Drift Analysis Report"))
	fmt.Fprintln(tw, r.bold("====================="))
	fmt.Fprintln(tw, r.bold("Status\tKind\tIdentifier"))
	fmt.Fprintln(tw, r.bold("------\t----\t----------"))

	driftCount, errorCount, missingCount, unmanagedCount, noDriftCount := 0, 0, 0, 0, 0

	for _, res := range results {
		if ctx.Err() != nil {
			_ = tw.Flush()
			return ctx.Err()
		}

		identifier, statusStr, detailsToPrintSeparately := r.processResultLine(res, &driftCount, &errorCount, &missingCount, &unmanagedCount, &noDriftCount)

		fmt.Fprintf(tw, "%s\t%s\t%s\n", statusStr, res.ResourceKind, identifier)

		if detailsToPrintSeparately != "" {
			_ = tw.Flush()
			r.printIndentedDetails(detailsToPrintSeparately)
			fmt.Fprintln(r.writer)
		}
	}

	_ = tw.Flush()

	r.printSummary(len(results), noDriftCount, driftCount, missingCount, unmanagedCount, errorCount)

	return nil
}

func (r *Reporter) processResultLine(res domain.ComparisonResult, driftCount, errorCount, missingCount, unmanagedCount, noDriftCount *int) (string, string, string) {
	identifier := res.SourceIdentifier
	statusStr := ""
	details := ""

	switch res.Status {
	case domain.StatusDrifted:
		*driftCount++
		statusStr = r.red("[DRIFT]")
		if identifier == "" {
			identifier = res.ProviderAssignedID
		}
		details = r.formatDriftDetails(res.Differences)
	case domain.StatusError:
		*errorCount++
		statusStr = r.magenta("[ERROR]")
		if identifier == "" {
			identifier = res.ProviderAssignedID
		}
		errMsg := fmt.Sprintf("Comparison failed: %v", res.Error)
		var appErr *apperrors.AppError
		if errors.As(res.Error, &appErr) && appErr.IsUserFacing {
			errMsg += fmt.Sprintf(" (%s)", appErr.Message)
		}
		details = r.magenta(errMsg)
	case domain.StatusMissing:
		*missingCount++
		statusStr = r.yellow("[MISSING]")
		details = r.yellow("Resource defined in state source but not found on platform.")
	case domain.StatusUnmanaged:
		*unmanagedCount++
		statusStr = r.cyan("[UNMANAGED]")
		identifier = res.ProviderAssignedID
		details = r.cyan("Resource found on platform but not defined in state source.")
	case domain.StatusNoDrift:
		*noDriftCount++
		statusStr = r.green("[OK]")
		if identifier == "" {
			identifier = res.ProviderAssignedID
		}
	default:
		statusStr = "[UNKNOWN]"
		if identifier == "" {
			identifier = res.ProviderAssignedID
		}
		details = "Unknown comparison status."
	}

	if identifier == "" {
		identifier = "<unknown>"
	}

	return identifier, statusStr, details
}

func (r *Reporter) printIndentedDetails(details string) {
	lines := strings.Split(details, "\n")
	for _, line := range lines {
		fmt.Fprintln(r.writer, "  "+line)
	}
}

func (r *Reporter) formatDriftDetails(diffs []domain.AttributeDiff) string {
	if len(diffs) == 0 {
		return r.yellow("Drift detected but no specific differences provided.")
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("%s attributes differ:", r.bold(len(diffs))))

	for i, diff := range diffs {
		builder.WriteString(fmt.Sprintf("\n[%d] Attribute: %s", i+1, r.bold(diff.AttributeName)))
		if diff.Details != "" && !isGenericMapSliceDetail(diff.Details) {
			builder.WriteString(fmt.Sprintf(" (%s)", diff.Details))
		}

		diffOutput := r.generateSpecificDiff(diff.ExpectedValue, diff.ActualValue)
		builder.WriteString(diffOutput)
	}
	return builder.String()
}

func isGenericMapSliceDetail(detail string) bool {
	return detail == "Slice contents differ" || detail == "Map contents differ" || strings.HasPrefix(detail, "Differences by")
}

func (r *Reporter) generateSpecificDiff(expected, actual any) string {
	var builder strings.Builder

	if isPrimitiveOrNil(expected) && isPrimitiveOrNil(actual) {
		builder.WriteString(fmt.Sprintf("\n  %s %v", r.diffDel("- Expected:"), formatValueSimple(expected)))
		builder.WriteString(fmt.Sprintf("\n  %s %v", r.diffAdd("+ Actual:  "), formatValueSimple(actual)))
		return builder.String()
	}

	expectedMap, eOk := convertToMapStringAny(expected)
	actualMap, aOk := convertToMapStringAny(actual)

	if eOk && aOk {
		mapDiffSummary := r.formatMapDifference(expectedMap, actualMap)
		if mapDiffSummary != "" {
			builder.WriteString(mapDiffSummary)
			return builder.String()
		}
	}

	builder.WriteString(r.generateUnifiedDiff(expected, actual))

	return builder.String()
}

func (r *Reporter) formatMapDifference(expected, actual map[string]any) string {
	var diffs []string
	keys := make(map[string]struct{})

	if expected == nil {
		expected = map[string]any{}
	}
	if actual == nil {
		actual = map[string]any{}
	}

	for k := range expected {
		keys[k] = struct{}{}
	}
	for k := range actual {
		keys[k] = struct{}{}
	}

	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	for _, k := range sortedKeys {
		expVal, expExists := expected[k]
		actVal, actExists := actual[k]

		if !reflect.DeepEqual(expVal, actVal) {
			if expExists && !actExists {
				diffs = append(diffs, fmt.Sprintf("%s: %s %v, %s %s", k, r.diffDel("expected"), formatValueSimple(expVal), r.diffAdd("actual"), r.diffAdd("<missing>")))
			} else if !expExists && actExists {
				diffs = append(diffs, fmt.Sprintf("%s: %s %s, %s %v", k, r.diffDel("expected"), r.diffDel("<missing>"), r.diffAdd("actual"), formatValueSimple(actVal)))
			} else {
				diffs = append(diffs, fmt.Sprintf("%s: %s %v, %s %v", k, r.diffDel("expected"), formatValueSimple(expVal), r.diffAdd("actual"), formatValueSimple(actVal)))
			}
		}
	}

	if len(diffs) == 0 {
		return ""
	}

	return "\n  Map Changes: " + strings.Join(diffs, "; ")
}

func convertToMapStringAny(value any) (map[string]any, bool) {
	if value == nil {
		return nil, true
	}
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Map {
		if v.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		m := make(map[string]any)
		iter := v.MapRange()
		for iter.Next() {
			m[iter.Key().String()] = iter.Value().Interface()
		}
		return m, true
	}
	return nil, false
}

func (r *Reporter) generateUnifiedDiff(expected, actual any) string {
	var builder strings.Builder

	expectedLines, errExp := valueToDiffLines(expected)
	actualLines, errAct := valueToDiffLines(actual)

	if errExp != nil || errAct != nil {
		builder.WriteString("\n  <Error generating diff: Could not format values>")
		if errExp != nil {
			builder.WriteString(fmt.Sprintf("\n  Expected (Error): %v", errExp))
		} else {
			builder.WriteString(fmt.Sprintf("\n  Expected: %v", formatValueSimple(expected)))
		}
		if errAct != nil {
			builder.WriteString(fmt.Sprintf("\n  Actual (Error): %v", errAct))
		} else {
			builder.WriteString(fmt.Sprintf("\n  Actual:   %v", formatValueSimple(actual)))
		}
		return builder.String()
	}

	unifiedDiff := difflib.UnifiedDiff{
		A:        expectedLines,
		B:        actualLines,
		FromFile: "Expected",
		ToFile:   "Actual",
		Context:  2,
	}
	diffString, err := difflib.GetUnifiedDiffString(unifiedDiff)
	if err != nil {
		builder.WriteString(fmt.Sprintf("\n  <Error generating diff string: %v>", err))
		builder.WriteString(fmt.Sprintf("\n  Fallback Expected: %v", formatValueSimple(expected)))
		builder.WriteString(fmt.Sprintf("\n  Fallback Actual:   %v", formatValueSimple(actual)))
		return builder.String()
	}

	diffLines := strings.Split(diffString, "\n")
	hasContent := false
	for _, line := range diffLines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "@@") || trimmedLine == "" {
			continue
		}
		hasContent = true
		indentedLine := "  " + line

		if strings.HasPrefix(line, "+") {
			builder.WriteString("\n" + r.diffAdd(indentedLine))
		} else if strings.HasPrefix(line, "-") {
			builder.WriteString("\n" + r.diffDel(indentedLine))
		} else {
			builder.WriteString("\n" + indentedLine)
		}
	}

	if !hasContent && diffString != "" {
		builder.WriteString("\n  <Diff contained no changes>")
	} else if diffString == "" {
		builder.WriteString("\n  <No textual difference in formatted values>")
	}

	return builder.String()
}

func isPrimitiveOrNil(value any) bool {
	if value == nil {
		return true
	}
	t := reflect.TypeOf(value)
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	default:
		return false
	}
}

func formatValueSimple(value any) string {
	if value == nil {
		return "<nil>"
	}
	if strVal, ok := value.(string); ok {
		return fmt.Sprintf("%q", strVal)
	}
	return fmt.Sprintf("%v", value)
}

func valueToDiffLines(value any) ([]string, error) {
	if value == nil {
		return []string{"<nil>"}, nil
	}

	strVal, ok := value.(string)
	if ok {
		return strings.Split(strVal, "\n"), nil
	}

	jsonBytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		strRepresentation := fmt.Sprintf("%+v", value)
		return []string{fmt.Sprintf("<Error marshaling: %v; Fallback: %s>", err, strRepresentation)}, err
	}

	return strings.Split(string(jsonBytes), "\n"), nil
}

func (r *Reporter) printSummary(total, ok, drifted, missing, unmanaged, errored int) {
	fmt.Fprintln(r.writer)
	fmt.Fprintln(r.writer, r.bold("Summary:"))
	fmt.Fprintln(r.writer, r.bold("-------"))

	summaryTw := tabwriter.NewWriter(r.writer, 0, 8, 1, ' ', 0)
	fmt.Fprintf(summaryTw, "Total Resources Processed:\t%d\n", total)
	fmt.Fprintf(summaryTw, "No Drift:\t%s\n", r.green(ok))
	fmt.Fprintf(summaryTw, "Drifted:\t%s\n", r.red(drifted))
	fmt.Fprintf(summaryTw, "Missing (State Only):\t%s\n", r.yellow(missing))
	fmt.Fprintf(summaryTw, "Unmanaged (Platform Only):\t%s\n", r.cyan(unmanaged))
	fmt.Fprintf(summaryTw, "Errors:\t%s\n", r.magenta(errored))
	_ = summaryTw.Flush()
}
