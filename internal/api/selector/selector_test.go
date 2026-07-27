package selector

import (
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/api/anthropic"
	"github.com/ArpitK24/forge/internal/api/openai"
	"github.com/ArpitK24/forge/internal/core"
)

// baseConfig returns a Config with the given (provider, apiBase)
// pair. Other fields are zero.
func baseConfig(provider, apiBase string) *core.Config {
	return &core.Config{
		Provider: provider,
		APIBase:  apiBase,
		Model:    "m",
	}
}

// TestSelect_DefaultIsOpenAI verifies the legacy default
// (no provider, no api-base) returns the OpenAI-compatible
// adapter so existing users don't see a behavior change.
func TestSelect_DefaultIsOpenAI(t *testing.T) {
	c := baseConfig("", "")
	p, err := Select(c, "k")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, ok := p.(*openai.Client); !ok {
		t.Errorf("default returned %T, want *openai.Client", p)
	}
}

// TestSelect_AnthropicHost verifies that an api.anthropic.com URL
// selects the Anthropic adapter even without an explicit
// --provider flag.
func TestSelect_AnthropicHost(t *testing.T) {
	c := baseConfig("", "https://api.anthropic.com")
	p, err := Select(c, "k")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, ok := p.(*anthropic.Client); !ok {
		t.Errorf("anthropic.com URL returned %T, want *anthropic.Client", p)
	}
}

// TestSelect_AnthropicHostNoScheme verifies the URL matching is
// scheme-agnostic — a user can supply just the host.
func TestSelect_AnthropicHostNoScheme(t *testing.T) {
	c := baseConfig("", "api.anthropic.com")
	p, err := Select(c, "k")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, ok := p.(*anthropic.Client); !ok {
		t.Errorf("api.anthropic.com (no scheme) returned %T, want *anthropic.Client", p)
	}
}

// TestSelect_NIMHostDefaultsToOpenAI verifies the NIM default URL
// still routes to the OpenAI adapter (NIM is OpenAI-compatible
// on the wire).
func TestSelect_NIMHostDefaultsToOpenAI(t *testing.T) {
	c := baseConfig("", "https://integrate.api.nvidia.com/v1")
	p, err := Select(c, "k")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, ok := p.(*openai.Client); !ok {
		t.Errorf("NIM host returned %T, want *openai.Client", p)
	}
}

// TestSelect_ExplicitProviderOverridesURL verifies that
// cfg.Provider wins over the URL heuristic. This is how a user
// points Forge at a non-canonical-host Anthropic-format proxy.
func TestSelect_ExplicitProviderOverridesURL(t *testing.T) {
	c := baseConfig("anthropic", "https://my-proxy.internal/v1")
	p, err := Select(c, "k")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, ok := p.(*anthropic.Client); !ok {
		t.Errorf("explicit 'anthropic' returned %T, want *anthropic.Client", p)
	}
}

// TestSelect_ExplicitOpenAIOnAnthropicHost verifies that an
// explicit "openai" override wins even when the URL looks
// Anthropic-shaped. A LiteLLM-with-openai-backend scenario.
func TestSelect_ExplicitOpenAIOnAnthropicHost(t *testing.T) {
	c := baseConfig("openai", "https://api.anthropic.com")
	p, err := Select(c, "k")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, ok := p.(*openai.Client); !ok {
		t.Errorf("explicit 'openai' on anthropic.com returned %T, want *openai.Client", p)
	}
}

// TestSelect_UnknownProviderFails verifies the selector surfaces
// a clear error for an unrecognized provider id rather than
// silently falling back.
func TestSelect_UnknownProviderFails(t *testing.T) {
	c := baseConfig("google", "")
	_, err := Select(c, "k")
	if err == nil {
		t.Fatal("Select: expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "google") {
		t.Errorf("error = %v, want it to mention the unknown provider id", err)
	}
}

// TestSelect_MissingAPIKeyFails verifies a missing key is
// rejected up front, matching the previous buildProvider behavior
// (no key, no provider).
func TestSelect_MissingAPIKeyFails(t *testing.T) {
	c := baseConfig("", "")
	_, err := Select(c, "")
	if err == nil {
		t.Fatal("Select: expected error for missing api key, got nil")
	}
}

// TestIsAnthropicHost exercises the URL-host matcher directly,
// covering scheme variants and trailing-path stripping.
func TestIsAnthropicHost(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://api.anthropic.com", true},
		{"http://api.anthropic.com", true},
		{"api.anthropic.com", true},
		{"https://api.anthropic.com/", true},
		{"https://api.anthropic.com/v1/messages", true},
		{"API.ANTHROPIC.COM", true}, // case-insensitive
		{"", false},
		{"https://integrate.api.nvidia.com/v1", false},
		{"https://anthropic.com", false},          // bare domain, not the api host
		{"https://my-proxy.anthropic.com", false}, // subdomain, not the api host
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := IsAnthropicHost(tc.in); got != tc.want {
				t.Errorf("IsAnthropicHost(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
