// Package selector builds the active api.Provider from a
// resolved Config. Lives in its own package so api/anthropic
// and api/openai (which both import api for the Provider
// interface and event types) don't pull the selector into a
// cycle. Callers — the TUI and the headless command — import
// this package, not the selector directly.
//
// Selection rule: explicit cfg.Provider wins; otherwise the URL
// host decides (api.anthropic.com → Anthropic, anything else →
// OpenAI-compatible). See internal/api/selector/selector.go for
// the full rules and rationale.
package selector

import (
	"fmt"
	"strings"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/api/anthropic"
	"github.com/ArpitK24/forge/internal/api/openai"
	"github.com/ArpitK24/forge/internal/core"
)

// Select builds the active api.Provider from the resolved
// configuration. Two selection signals are consulted:
//
//  1. cfg.APIBase — if the URL host is "api.anthropic.com" (with
//     or without scheme), use the Anthropic adapter. The NIM
//     default ("integrate.api.nvidia.com") and any other
//     OpenAI-compatible host (openai.com, openrouter.ai,
//     localhost ollama, internal gateways) fall through to the
//     OpenAI-compatible adapter.
//
//  2. cfg.Provider — an explicit provider id, e.g. "anthropic" or
//     "openai", overrides the URL heuristic. This is how a user
//     points Forge at a non-Anthropic-hosted Anthropic-format
//     proxy (a LiteLLM-with-anthropic-backend, for example), or
//     forces OpenAI-compatible even when the URL looks
//     Anthropic-shaped.
//
// Order: Provider > APIBase > default OpenAI-compatible.
func Select(cfg *core.Config, apiKey string) (api.Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no API key found")
	}

	family := detectFamily(cfg)
	base := cfg.APIBase
	if base == "" {
		base = defaultBaseFor(family)
	}
	model := cfg.EffectiveModel()

	switch family {
	case core.ProviderAnthropic:
		return anthropic.New(base, apiKey, model), nil
	case core.ProviderOpenAI, "":
		return openai.New(base, apiKey, model), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want 'anthropic' or 'openai')", family)
	}
}

// detectFamily picks the provider id using the rules documented
// on Select. Exposed for tests via DetectFamily.
func detectFamily(cfg *core.Config) string {
	if cfg.Provider != "" {
		return cfg.Provider
	}
	if IsAnthropicHost(cfg.APIBase) {
		return core.ProviderAnthropic
	}
	return core.ProviderOpenAI
}

// DetectFamily is the exported wrapper around detectFamily so
// tests outside the package can exercise it.
func DetectFamily(cfg *core.Config) string {
	return detectFamily(cfg)
}

// IsAnthropicHost returns true when the URL host is Anthropic's
// first-party endpoint. We only match the canonical
// "api.anthropic.com" host — not "anthropic.com" in general —
// because users may legitimately point Forge at a self-hosted
// proxy under a non-anthropic.com domain, and we don't want to
// silently pick the Anthropic adapter for those.
//
// Matching is scheme-agnostic ("https://api.anthropic.com" and
// "api.anthropic.com" both match) and case-insensitive on the
// host.
func IsAnthropicHost(apiBase string) bool {
	if apiBase == "" {
		return false
	}
	host := apiBase
	// Strip scheme.
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	// Strip path and port.
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	return strings.EqualFold(host, "api.anthropic.com")
}

// defaultBaseFor returns the per-provider default API base URL
// when the caller hasn't supplied one. Mirrors the previous
// behavior (cfg.APIBase defaulted to NIM) for the OpenAI path;
// Anthropic has a single canonical host so it's a one-liner.
func defaultBaseFor(family string) string {
	if family == core.ProviderAnthropic {
		return anthropic.DefaultAPIBase
	}
	return core.DefaultAPIBase
}
