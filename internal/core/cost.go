// Package core, cost.go — usage accumulation and per-model USD
// cost estimation. Per spec §2.8, the full cost tracker is a
// lock-free atomic counter with a static pricing table.
//
// Phase 2 ships just enough to accumulate input/output tokens
// across all turns in a session and to look up a per-model
// per-million-token price. The full UI integration (per-turn
// cost line, history queries) lands in later phases — the
// shape is here so future phases don't have to break callers.
package core

import (
	"strings"
	"sync/atomic"
)

// ModelPricing is the per-million-token USD price table for a
// single model tier. Per spec §2.8. The dollar figures are
// obviously stale at the moment of reading — the table is meant
// to be updatable, not permanent. Phase 4 adds a /cost command
// that can refresh the table from a network source.
type ModelPricing struct {
	// ModelSubstring is the lowercase substring used to match a
	// model id (e.g. "llama-3.3-70b" matches "meta/llama-3.3-70b-instruct").
	ModelSubstring string `json:"model_substring"`
	// InputPerMtok is the USD price per million input tokens.
	// "Input" here means non-cached input; cache reads usually
	// have their own (cheaper) line.
	InputPerMtok float64 `json:"input_per_mtok"`
	// OutputPerMtok is the USD price per million output tokens.
	OutputPerMtok float64 `json:"output_per_mtok"`
	// CacheCreationPerMtok is the USD price per million cache-creation
	// tokens. Zero for providers that don't bill this separately.
	CacheCreationPerMtok float64 `json:"cache_creation_per_mtok,omitempty"`
	// CacheReadPerMtok is the USD price per million cache-read tokens.
	// Zero for providers that don't bill this separately.
	CacheReadPerMtok float64 `json:"cache_read_per_mtok,omitempty"`
}

// defaultPricingTable is the built-in, conservative estimate table.
// Real prices change constantly; the goal here is "good enough to
// render a sensible USD figure, not a billing source of truth."
// Tier names loosely map to NIM's hosted Llama lineup.
var defaultPricingTable = []ModelPricing{
	// Llama 3.3 70B is NIM's mid-tier; pricing is approximate
	// (NIM's pricing changes per region and per agreement).
	{ModelSubstring: "llama-3.3-70b", InputPerMtok: 0.59, OutputPerMtok: 0.79},
	// 3.1 70B is the previous generation; cheaper.
	{ModelSubstring: "llama-3.1-70b", InputPerMtok: 0.59, OutputPerMtok: 0.79},
	// 8B is the cheap/fast tier.
	{ModelSubstring: "llama-3.1-8b", InputPerMtok: 0.18, OutputPerMtok: 0.18},
	// OpenAI gpt-oss-120b on NIM.
	{ModelSubstring: "gpt-oss-120b", InputPerMtok: 0.15, OutputPerMtok: 0.60},
	// Anthropic Claude (used when an Anthropic adapter lands in Phase 4).
	{ModelSubstring: "claude-sonnet-4-5", InputPerMtok: 3.0, OutputPerMtok: 15.0},
	{ModelSubstring: "claude-haiku-4-5", InputPerMtok: 0.80, OutputPerMtok: 4.0},
	{ModelSubstring: "claude-opus-4-7", InputPerMtok: 15.0, OutputPerMtok: 75.0},
	// Catch-all for unknown current-generation models. Conservative
	// — over-estimates slightly rather than under-charging.
	{ModelSubstring: "__default__", InputPerMtok: 1.0, OutputPerMtok: 3.0},
}

// PricingFor returns the pricing row that best matches a model id
// (case-insensitive substring match). Falls back to the __default__
// row if no specific match is found. The first match wins; the
// table is ordered most-specific-first.
func PricingFor(modelID string) ModelPricing {
	id := strings.ToLower(modelID)
	var fallback ModelPricing
	for _, p := range defaultPricingTable {
		if p.ModelSubstring == "__default__" {
			fallback = p
			continue
		}
		if strings.Contains(id, p.ModelSubstring) {
			return p
		}
	}
	return fallback
}

// EstimateCostUSD returns the estimated USD cost of a UsageInfo
// against a given model's pricing row. Cache fields default to
// the input rate when the model's pricing row doesn't distinguish
// them (most providers bill cache reads at the same rate or
// cheaper; the worst case is "same as input" so we use that).
func EstimateCostUSD(u UsageInfo, modelID string) float64 {
	p := PricingFor(modelID)
	million := 1_000_000.0
	in := float64(u.InputTokens) / million
	out := float64(u.OutputTokens) / million
	cacheCreate := float64(u.CacheCreationInputTokens) / million
	cacheRead := float64(u.CacheReadInputTokens) / million

	// If the provider's pricing doesn't break out cache rates,
	// fall back to the input rate (worst case is "same as input").
	cc := p.CacheCreationPerMtok
	if cc == 0 {
		cc = p.InputPerMtok
	}
	cr := p.CacheReadPerMtok
	if cr == 0 {
		cr = p.InputPerMtok
	}

	return in*p.InputPerMtok +
		out*p.OutputPerMtok +
		cacheCreate*cc +
		cacheRead*cr
}

// CostTracker accumulates token usage across a session. Spec
// §2.8: "lock-free atomic counters for input tokens, output
// tokens, cache-creation tokens, cache-read tokens." This is
// the Phase 2 shape — it counts tokens and can render a one-line
// summary; the full history query and per-turn detail comes in
// later phases.
type CostTracker struct {
	inputTokens              atomic.Int64
	outputTokens             atomic.Int64
	cacheCreationInputTokens atomic.Int64
	cacheReadInputTokens     atomic.Int64
	callCount                atomic.Int64
	// LastModel is the most recent model id used. We don't lock
	// it because it's a string — assignment of a string header
	// is word-sized and not torn on amd64. Worst case: a stale
	// model name in Summary() for one call. Phase 4 can wrap
	// it in an atomic.Value if it matters.
	LastModel string
}

// NewCostTracker constructs an empty CostTracker.
func NewCostTracker() *CostTracker {
	return &CostTracker{}
}

// AddUsage atomically adds a single turn's usage to the totals.
// Safe to call from any goroutine.
func (c *CostTracker) AddUsage(u UsageInfo, modelID string) {
	if c == nil {
		return
	}
	c.inputTokens.Add(int64(u.InputTokens))
	c.outputTokens.Add(int64(u.OutputTokens))
	c.cacheCreationInputTokens.Add(int64(u.CacheCreationInputTokens))
	c.cacheReadInputTokens.Add(int64(u.CacheReadInputTokens))
	c.callCount.Add(1)
	c.LastModel = modelID
}

// Totals returns the accumulated totals as a UsageInfo. The model
// field is not populated (this is totals, not a per-call record).
func (c *CostTracker) Totals() UsageInfo {
	if c == nil {
		return UsageInfo{}
	}
	return UsageInfo{
		InputTokens:              int(c.inputTokens.Load()),
		OutputTokens:             int(c.outputTokens.Load()),
		CacheCreationInputTokens: int(c.cacheCreationInputTokens.Load()),
		CacheReadInputTokens:     int(c.cacheReadInputTokens.Load()),
	}
}

// Calls returns the number of AddUsage invocations.
func (c *CostTracker) Calls() int {
	if c == nil {
		return 0
	}
	return int(c.callCount.Load())
}

// Reset zeros every counter. Used by the headless retry
// wrapper so a failed attempt's usage doesn't double-count
// against a successful retry.
func (c *CostTracker) Reset() {
	if c == nil {
		return
	}
	c.inputTokens.Store(0)
	c.outputTokens.Store(0)
	c.cacheCreationInputTokens.Store(0)
	c.cacheReadInputTokens.Store(0)
	c.callCount.Store(0)
	c.LastModel = ""
}

// TotalCostUSD returns the estimated USD cost of the accumulated
// usage against the last-used model.
func (c *CostTracker) TotalCostUSD() float64 {
	if c == nil {
		return 0
	}
	return EstimateCostUSD(c.Totals(), c.LastModel)
}

// Summary renders a one-line human summary suitable for the
// post-turn footer line. Format:
//
//	"3 turns · 4.2k in / 1.1k out · $0.012"
//
// If the model is empty, omits the cost portion.
func (c *CostTracker) Summary() string {
	if c == nil {
		return ""
	}
	t := c.Totals()
	turns := c.Calls()
	cost := c.TotalCostUSD()
	if c.LastModel == "" {
		return formatTotalsLine(turns, t, -1)
	}
	_ = cost // computed via EstimateCostUSD inside formatTotalsLine
	return formatTotalsLine(turns, t, EstimateCostUSD(t, c.LastModel))
}

func formatTotalsLine(turns int, t UsageInfo, costUSD float64) string {
	if turns == 0 {
		return "0 turns"
	}
	in := formatTokenCount(t.InputTokens + t.CacheCreationInputTokens + t.CacheReadInputTokens)
	out := formatTokenCount(t.OutputTokens)
	if costUSD < 0 {
		return formatInt(turns) + " turn" + plural(turns) + " · " + in + " in / " + out + " out"
	}
	return formatInt(turns) + " turn" + plural(turns) +
		" · " + in + " in / " + out + " out" +
		" · $" + formatCost(costUSD)
}

func formatTokenCount(n int) string {
	if n >= 1000 {
		// Keep one decimal of thousands — 4.2k is the convention.
		return formatFloat(float64(n)/1000.0, 1) + "k"
	}
	return formatInt(n)
}

func formatInt(n int) string {
	// Lightweight int formatter that adds thousands separators.
	// Avoids pulling in fmt for a hot path used once per turn.
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	digits := 0
	for n > 0 {
		if digits > 0 && digits%3 == 0 {
			i--
			buf[i] = ','
		}
		i--
		buf[i] = byte('0' + n%10)
		digits++
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func formatFloat(f float64, decimals int) string {
	// Single-decimal float printer. Avoids fmt for the hot path.
	// We don't need scientific notation at the magnitudes we
	// display (token counts, USD amounts).
	mul := 1.0
	for i := 0; i < decimals; i++ {
		mul *= 10
	}
	rounded := int64(f*mul + 0.5)
	intPart := rounded / int64(mul)
	fracPart := rounded % int64(mul)
	if fracPart < 0 {
		fracPart = -fracPart
	}
	s := formatInt(int(intPart))
	if decimals == 0 {
		return s
	}
	pad := formatInt(int(fracPart))
	for len(pad) < decimals {
		pad = "0" + pad
	}
	return s + "." + pad
}

func formatCost(usd float64) string {
	// USD with 4 decimals — small enough to show meaningful
	// fractions on a 4-cent run, big enough not to lie about
	// sub-cent precision.
	return formatFloat(usd, 4)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
