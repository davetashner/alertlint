// Package cloudwatch implements ConfigProvider over the CloudWatch
// DescribeAlarms API (docs/specs/provider-adapters.md §7).
//
// CloudWatch alarms have no query string, so condition_raw is a canonical
// rendering of namespace, metric, statistic, and dimensions — verbatim in
// content, synthesized in shape. Dimensions pass through as identity-hint
// tags (dimension name → value); severity does not exist in CloudWatch
// and is always "unknown".
//
// The AWS client is consumed through a one-method interface, so tests
// mock the API at the SDK boundary — no HTTP fixtures, no credentials.
package cloudwatch

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

const providerID = "cloudwatch"

// DescribeAlarmsAPI is the slice of the CloudWatch client this adapter
// uses; the SDK's *cloudwatch.Client satisfies it, and tests fake it.
type DescribeAlarmsAPI interface {
	DescribeAlarms(ctx context.Context, params *cloudwatch.DescribeAlarmsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error)
}

// Adapter pulls metric alarms.
type Adapter struct {
	Client DescribeAlarmsAPI
}

// ProviderID implements adapter.Provider.
func (a *Adapter) ProviderID() string { return providerID }

// SchemaVersion implements adapter.Provider.
func (a *Adapter) SchemaVersion() string { return model.CanonicalSchemaVersion }

// FetchConfigs implements adapter.ConfigProvider: one AlertConfig per
// metric alarm, paginated via NextToken, sorted by alarm name for
// deterministic iteration.
func (a *Adapter) FetchConfigs(scope adapter.Scope, _ adapter.TimeWindow) iter.Seq2[model.AlertConfig, error] {
	return func(yield func(model.AlertConfig, error) bool) {
		var alarms []types.MetricAlarm
		var next *string
		for {
			input := &cloudwatch.DescribeAlarmsInput{NextToken: next}
			if scope.Selector != "" {
				prefix := scope.Selector // opaque provider-native narrowing
				input.AlarmNamePrefix = &prefix
			}
			out, err := a.Client.DescribeAlarms(context.Background(), input)
			if err != nil {
				var zero model.AlertConfig
				yield(zero, fmt.Errorf("cloudwatch DescribeAlarms: %w", err))
				return
			}
			alarms = append(alarms, out.MetricAlarms...)
			if out.NextToken == nil {
				break
			}
			next = out.NextToken
		}
		sort.Slice(alarms, func(i, j int) bool { return deref(alarms[i].AlarmName) < deref(alarms[j].AlarmName) })
		for _, alarm := range alarms {
			if !yield(a.toConfig(alarm, scope), nil) {
				return
			}
		}
	}
}

func (a *Adapter) toConfig(alarm types.MetricAlarm, scope adapter.Scope) model.AlertConfig {
	name := deref(alarm.AlarmName)
	cfg := model.AlertConfig{
		Envelope: model.Envelope{
			SchemaVersion: model.CanonicalSchemaVersion,
			Source:        model.Source{Provider: providerID, Tenant: scope.Tenant},
			SourceRef:     model.SourceRef{Kind: "alarm", NativeID: name},
			IdentityHints: model.IdentityHints{
				Tags:         map[string]string{},
				Names:        []string{name},
				ExternalRefs: []model.ExternalRef{},
			},
		},
		Name:         name,
		ConditionRaw: renderCondition(alarm),
		Severity:     model.Severity{Native: "", Normalized: model.SeverityUnknown},
		Routing:      []model.Route{},
		Status:       model.StatusEnabled,
	}
	if arn := deref(alarm.AlarmArn); arn != "" {
		u := arn
		cfg.SourceRef.URL = &u
	}
	if alarm.ActionsEnabled != nil && !*alarm.ActionsEnabled {
		// Actions disabled is CloudWatch's silence idiom: the alarm still
		// evaluates but pages nobody (REQ-HIST-002 distinguishability).
		cfg.Status = model.StatusSilenced
	}
	for _, dim := range alarm.Dimensions {
		cfg.IdentityHints.Tags[deref(dim.Name)] = deref(dim.Value)
	}
	for _, action := range alarm.AlarmActions {
		cfg.Routing = append(cfg.Routing, model.Route{TargetKind: model.RouteOther, Target: action})
	}
	if alarm.Threshold != nil {
		v := *alarm.Threshold
		cfg.Threshold = &v
	}
	if comp := comparatorFrom(alarm.ComparisonOperator); comp != "" {
		c := comp
		cfg.Comparator = &c
	}
	if alarm.Period != nil && alarm.EvaluationPeriods != nil {
		secs := int64(*alarm.Period) * int64(*alarm.EvaluationPeriods)
		cfg.DurationS = &secs
	}
	if alarm.AlarmConfigurationUpdatedTimestamp != nil {
		u := alarm.AlarmConfigurationUpdatedTimestamp.UTC()
		cfg.UpdatedAt = &u
	}
	return cfg
}

// renderCondition builds the canonical condition string; CloudWatch has
// no native query text to carry verbatim.
func renderCondition(alarm types.MetricAlarm) string {
	var dims []string
	for _, d := range alarm.Dimensions {
		dims = append(dims, deref(d.Name)+"="+deref(d.Value))
	}
	sort.Strings(dims)
	cond := fmt.Sprintf("%s(%s/%s{%s})", alarm.Statistic, deref(alarm.Namespace), deref(alarm.MetricName), strings.Join(dims, ","))
	if alarm.Threshold != nil {
		cond += fmt.Sprintf(" %s %g", symbolFor(alarm.ComparisonOperator), *alarm.Threshold)
	}
	return cond
}

func comparatorFrom(op types.ComparisonOperator) string {
	switch op {
	case types.ComparisonOperatorGreaterThanThreshold:
		return ">"
	case types.ComparisonOperatorGreaterThanOrEqualToThreshold:
		return ">="
	case types.ComparisonOperatorLessThanThreshold:
		return "<"
	case types.ComparisonOperatorLessThanOrEqualToThreshold:
		return "<="
	}
	return "" // anomaly-detection band operators have no scalar comparator
}

func symbolFor(op types.ComparisonOperator) string {
	if s := comparatorFrom(op); s != "" {
		return s
	}
	return string(op)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
