package core

import (
	"strings"
	"testing"
)

func TestUsageTotalAndTotalInput(t *testing.T) {
	u := UsageInfo{InputTokens: 100, OutputTokens: 50}
	if got := u.TotalInput(); got != 100 {
		t.Errorf("TotalInput = %d, want 100", got)
	}
	if got := u.Total(); got != 150 {
		t.Errorf("Total = %d, want 150", got)
	}
}

func TestPricingForKnownModel(t *testing.T) {
	cases := []struct {
		modelID string
		wantSub string // a unique substring of the expected row's ModelSubstring
	}{
		{"meta/llama-3.3-70b-instruct", "llama-3.3-70b"},
		{"meta/llama-3.1-8b-instruct", "llama-3.1-8b"},
		{"openai/gpt-oss-120b", "gpt-oss-120b"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"random-unknown-model", "__default__"},
		{"", "__default__"},
	}
	for _, tc := range cases {
		t.Run(tc.modelID, func(t *testing.T) {
			p := PricingFor(tc.modelID)
			if !strings.Contains(p.ModelSubstring, tc.wantSub) {
				t.Errorf("PricingFor(%q).ModelSubstring = %q, want contains %q",
					tc.modelID, p.ModelSubstring, tc.wantSub)
			}
		})
	}
}

func TestPricingForIsCaseInsensitive(t *testing.T) {
	upper := PricingFor("META/LLAMA-3.3-70B-INSTRUCT")
	lower := PricingFor("meta/llama-3.3-70b-instruct")
	if upper.InputPerMtok != lower.InputPerMtok {
		t.Errorf("case sensitivity changed pricing: upper=%+v lower=%+v",
			upper, lower)
	}
}

func TestEstimateCostUSD(t *testing.T) {
	// 1M input tokens at $1/MTok + 1M output tokens at $3/MTok = $4.
	u := UsageInfo{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	got := EstimateCostUSD(u, "unknown-model-xyz")
	want := 1.0*1.0 + 1.0*3.0
	if got != want {
		t.Errorf("EstimateCostUSD = %f, want %f", got, want)
	}
}

func TestEstimateCostUSDWithCache(t *testing.T) {
	// We don't have an "explicit cache prices" pricing row in the
	// default table, so the function falls back to the input rate
	// for both cache-create and cache-read. That means a 1M-tokens-each
	// Sonnet run with cache hits costs:
	//   input:   1M * $3.0  = $3.0
	//   output:  1M * $15.0 = $15.0
	//   cache-create (1M) * input-rate($3) = $3.0
	//   cache-read   (1M) * input-rate($3) = $3.0
	// total: $24.0
	u := UsageInfo{
		InputTokens:              1_000_000,
		OutputTokens:             1_000_000,
		CacheCreationInputTokens: 1_000_000,
		CacheReadInputTokens:     1_000_000,
	}
	got := EstimateCostUSD(u, "claude-sonnet-4-5")
	want := 24.0
	if !approxEqual(got, want, 0.001) {
		t.Errorf("EstimateCostUSD with cache = %f, want %f", got, want)
	}

	// And the math: when we set explicit cache prices via the
	// helper, they should be honored, not fallen back to input rate.
	manualRow := ModelPricing{
		ModelSubstring:        "test-explicit-cache",
		InputPerMtok:          1.0,
		OutputPerMtok:         2.0,
		CacheCreationPerMtok:  5.0,
		CacheReadPerMtok:      0.5,
	}
	// Replace the default table temporarily by pricing for the
	// test substring: this works because PricingFor does substring
	// match against a single known string, and our row wins.
	// We do this by giving the function a model id that matches
	// the test row by inserting it into the search.
	// Easiest path: test the math by constructing a UsageInfo
	// whose cost we'd compute by hand, then check via the
	// pricing table's public surface only.
	_ = manualRow // (intentionally not used — see below)
}

func TestEstimateCostUSDHonorsExplicitCachePrices(t *testing.T) {
	// We test the cache-price code path by injecting a pricing
	// row into the package-level table and using a unique
	// model id. We restore the original table at the end.
	original := defaultPricingTable
	defer func() { defaultPricingTable = original }()

	defaultPricingTable = []ModelPricing{
		{ModelSubstring: "test-row", InputPerMtok: 1.0, OutputPerMtok: 2.0,
			CacheCreationPerMtok: 5.0, CacheReadPerMtok: 0.5},
	}

	u := UsageInfo{
		InputTokens:              1_000_000,
		OutputTokens:             1_000_000,
		CacheCreationInputTokens: 1_000_000,
		CacheReadInputTokens:     1_000_000,
	}
	got := EstimateCostUSD(u, "test-row")
	// 1*1 + 1*2 + 1*5 + 1*0.5 = 8.5
	want := 8.5
	if !approxEqual(got, want, 0.001) {
		t.Errorf("EstimateCostUSD = %f, want %f (cache prices honored)", got, want)
	}
}

func TestCostTrackerAddUsageAccumulates(t *testing.T) {
	ct := NewCostTracker()
	ct.AddUsage(UsageInfo{InputTokens: 100, OutputTokens: 50}, "model-a")
	ct.AddUsage(UsageInfo{InputTokens: 200, OutputTokens: 100}, "model-a")

	totals := ct.Totals()
	if totals.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", totals.InputTokens)
	}
	if totals.OutputTokens != 150 {
		t.Errorf("OutputTokens = %d, want 150", totals.OutputTokens)
	}
	if ct.Calls() != 2 {
		t.Errorf("Calls = %d, want 2", ct.Calls())
	}
	if ct.LastModel != "model-a" {
		t.Errorf("LastModel = %q, want model-a", ct.LastModel)
	}
}

func TestCostTrackerNilSafe(t *testing.T) {
	// All methods on a nil *CostTracker should be no-ops, not panics.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil *CostTracker panicked: %v", r)
		}
	}()
	var ct *CostTracker
	ct.AddUsage(UsageInfo{InputTokens: 1}, "x")
	_ = ct.Totals()
	_ = ct.Calls()
	_ = ct.TotalCostUSD()
	_ = ct.Summary()
}

func TestCostTrackerSummaryFormat(t *testing.T) {
	ct := NewCostTracker()
	ct.AddUsage(UsageInfo{InputTokens: 4200, OutputTokens: 1100}, "meta/llama-3.3-70b-instruct")
	summary := ct.Summary()
	// Format: "1 turn · 4.2k in / 1.1k out · $0.XXXX"
	if !strings.Contains(summary, "turn") {
		t.Errorf("Summary missing 'turn': %q", summary)
	}
	if !strings.Contains(summary, "in / ") {
		t.Errorf("Summary missing 'in / ': %q", summary)
	}
	if !strings.Contains(summary, "$") {
		t.Errorf("Summary missing $: %q", summary)
	}
}

func TestCostTrackerReset(t *testing.T) {
	ct := NewCostTracker()
	ct.AddUsage(UsageInfo{InputTokens: 100, OutputTokens: 50}, "model-a")
	ct.AddUsage(UsageInfo{InputTokens: 200, OutputTokens: 100}, "model-a")
	if ct.Calls() != 2 {
		t.Errorf("pre-reset Calls = %d, want 2", ct.Calls())
	}
	if ct.Totals().InputTokens != 300 {
		t.Errorf("pre-reset InputTokens = %d, want 300", ct.Totals().InputTokens)
	}
	ct.Reset()
	if ct.Calls() != 0 {
		t.Errorf("post-reset Calls = %d, want 0", ct.Calls())
	}
	if ct.Totals().InputTokens != 0 {
		t.Errorf("post-reset InputTokens = %d, want 0", ct.Totals().InputTokens)
	}
	if ct.LastModel != "" {
		t.Errorf("post-reset LastModel = %q, want ''", ct.LastModel)
	}
	// Nil-safe.
	ct.Reset()
	var nilCT *CostTracker
	nilCT.Reset() // must not panic
}

func TestFormatIntAndFloat(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
		{1234567, "1,234,567"},
	}
	for _, tc := range cases {
		if got := formatInt(tc.in); got != tc.want {
			t.Errorf("formatInt(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}

	fcases := []struct {
		in     float64
		dec    int
		want   string
	}{
		{1.0, 1, "1.0"},
		{1.25, 1, "1.3"}, // bankers? No — we round half up
		{4.2, 1, "4.2"},
		{0.01234, 4, "0.0123"},
	}
	for _, tc := range fcases {
		if got := formatFloat(tc.in, tc.dec); got != tc.want {
			t.Errorf("formatFloat(%f, %d) = %q, want %q", tc.in, tc.dec, got, tc.want)
		}
	}
}

func approxEqual(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
