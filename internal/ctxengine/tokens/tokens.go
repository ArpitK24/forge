// Package tokens is the context engine's deterministic token estimator.
//
// Milestone 0 deliberately uses ceil(bytes/4) instead of a real BPE
// tokenizer: the pure-Go cl100k/o200k ports either download their merge
// tables at runtime (network in tests, non-hermetic CI) or vendor
// multi-megabyte tables into the binary, and the budget allocator only
// needs a stable, monotone estimate. Section 15 of the design calls for
// online calibration against UsageInfo.InputTokens later.
package tokens

// Name identifies the estimator in eval output so baselines computed
// with a different estimator are never compared silently.
const Name = "bytes/4"

// Estimate returns ceil(len(s)/4).
func Estimate(s string) int { return (len(s) + 3) / 4 }

// EstimateBytes is Estimate for a byte length.
func EstimateBytes(n int) int { return (n + 3) / 4 }
