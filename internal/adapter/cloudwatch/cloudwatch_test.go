package cloudwatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/adapter/adaptertest"
	"github.com/davetashner/alertlint/internal/model"
)

// fakeClient mocks the SDK at the interface boundary: pages of alarms,
// optionally an error partway.
type fakeClient struct {
	pages [][]types.MetricAlarm
	err   error
	calls int
}

func (f *fakeClient) DescribeAlarms(_ context.Context, params *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error) {
	if params.NextToken == nil {
		f.calls = 0 // a fresh pull starts from page zero, like the real API
	}
	if f.err != nil && f.calls > 0 {
		return nil, f.err
	}
	page := f.calls
	f.calls++
	out := &cloudwatch.DescribeAlarmsOutput{MetricAlarms: f.pages[page]}
	if page+1 < len(f.pages) {
		token := "next"
		out.NextToken = &token
	}
	return out, nil
}

func strp(s string) *string   { return &s }
func f64p(v float64) *float64 { return &v }
func i32p(v int32) *int32     { return &v }
func boolp(v bool) *bool      { return &v }

func alarms() [][]types.MetricAlarm {
	updated := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	return [][]types.MetricAlarm{
		{
			{
				AlarmName:  strp("checkout-api-p99-latency"),
				AlarmArn:   strp("arn:aws:cloudwatch:us-east-1:1:alarm:checkout-api-p99-latency"),
				Namespace:  strp("AWS/ApplicationELB"),
				MetricName: strp("TargetResponseTime"),
				Statistic:  types.StatisticAverage,
				Dimensions: []types.Dimension{
					{Name: strp("LoadBalancer"), Value: strp("app/checkout/abc")},
					{Name: strp("service"), Value: strp("checkout-api")},
				},
				Threshold:                          f64p(2.5),
				ComparisonOperator:                 types.ComparisonOperatorGreaterThanThreshold,
				Period:                             i32p(60),
				EvaluationPeriods:                  i32p(10),
				ActionsEnabled:                     boolp(true),
				AlarmActions:                       []string{"arn:aws:sns:us-east-1:1:pages"},
				AlarmConfigurationUpdatedTimestamp: &updated,
			},
		},
		{
			{
				AlarmName:          strp("batch-queue-depth-muted"),
				Namespace:          strp("AWS/SQS"),
				MetricName:         strp("ApproximateNumberOfMessagesVisible"),
				Statistic:          types.StatisticMaximum,
				Dimensions:         []types.Dimension{{Name: strp("QueueName"), Value: strp("batch-jobs")}},
				Threshold:          f64p(1000),
				ComparisonOperator: types.ComparisonOperatorGreaterThanOrEqualToThreshold,
				Period:             i32p(300),
				EvaluationPeriods:  i32p(2),
				ActionsEnabled:     boolp(false),
			},
		},
	}
}

func window() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func TestConformance(t *testing.T) {
	a := &Adapter{Client: &fakeClient{pages: alarms()}}
	adaptertest.Run(t, a, adapter.Scope{Tenant: "123456789012/us-east-1"}, window())
}

func TestConfigMapping(t *testing.T) {
	a := &Adapter{Client: &fakeClient{pages: alarms()}}
	var cfgs []model.AlertConfig
	for c, err := range a.FetchConfigs(adapter.Scope{Tenant: "acct"}, window()) {
		if err != nil {
			t.Fatal(err)
		}
		cfgs = append(cfgs, c)
	}
	if len(cfgs) != 2 {
		t.Fatalf("configs = %d, want 2 (both pages)", len(cfgs))
	}

	// Sorted by name: batch first, checkout second.
	c := cfgs[1]
	if c.SourceRef.NativeID != "checkout-api-p99-latency" || c.SourceRef.Kind != "alarm" {
		t.Errorf("source_ref = %+v", c.SourceRef)
	}
	if c.Threshold == nil || *c.Threshold != 2.5 || c.Comparator == nil || *c.Comparator != ">" {
		t.Errorf("extraction = %v %v", c.Threshold, c.Comparator)
	}
	if c.DurationS == nil || *c.DurationS != 600 {
		t.Errorf("duration_s = %v, want 600 (60s × 10 periods)", c.DurationS)
	}
	if c.IdentityHints.Tags["service"] != "checkout-api" {
		t.Errorf("dimension hints = %v", c.IdentityHints.Tags)
	}
	if !strings.Contains(c.ConditionRaw, "AWS/ApplicationELB/TargetResponseTime") || !strings.Contains(c.ConditionRaw, "> 2.5") {
		t.Errorf("condition_raw = %q", c.ConditionRaw)
	}
	if len(c.Routing) != 1 || c.Routing[0].TargetKind != model.RouteOther {
		t.Errorf("routing = %+v", c.Routing)
	}
	if c.Severity.Normalized != model.SeverityUnknown {
		t.Errorf("severity = %+v — CloudWatch has none", c.Severity)
	}
	if c.UpdatedAt == nil {
		t.Error("updated_at lost")
	}

	// Actions-disabled alarm reads as silenced (REQ-HIST-002).
	if cfgs[0].Status != model.StatusSilenced {
		t.Errorf("actions-disabled alarm status = %s, want silenced", cfgs[0].Status)
	}
}

func TestErrorsAreNotEmptyResults(t *testing.T) {
	a := &Adapter{Client: &fakeClient{pages: alarms(), err: errors.New("Throttling: rate exceeded")}}
	var got error
	var count int
	for _, err := range a.FetchConfigs(adapter.Scope{Tenant: "acct"}, window()) {
		if err != nil {
			got = err
			break
		}
		count++
	}
	if got == nil || !strings.Contains(got.Error(), "Throttling") {
		t.Fatalf("throttled pull must surface an error, got %v after %d records", got, count)
	}
}
