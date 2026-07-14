// Package api defines the canonical request/response model Forge
// uses to talk to any model provider, plus the Provider interface
// every adapter implements.
//
// The shape of this package is governed by spec §4.1, §4.2, §4.3.
// In one sentence: a Provider is anything that takes a Request
// and returns a streaming channel of StreamEvents; everything
// downstream of that (query loop, TUI, ACP, bridge) works only
// against this canonical surface and never against a vendor's
// raw wire format.
//
// Dependency direction (per spec §1): api depends on core, and
// nothing in core depends on api. Provider adapters live in
// sub-packages (e.g. internal/api/openai) and depend on both
// api and core.
package api
