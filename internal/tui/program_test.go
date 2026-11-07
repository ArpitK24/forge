package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// hitServer counts requests and answers each with a 500 so the
// provider gives up quickly.
func hitServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "test server", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func streamOnce(t *testing.T, p api.Provider) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events, errs := p.Stream(ctx, api.Request{
		Model:     "fake-model",
		MaxTokens: 16,
		Messages:  []core.Message{core.NewUserText("hi")},
	})
	for range events {
	}
	for range errs {
	}
}

// TestBuildProvider_HonoursForgeAPIBase: the TUI must use
// FORGE_API_BASE when --api-base is not given, like headless.
func TestBuildProvider_HonoursForgeAPIBase(t *testing.T) {
	srv, hits := hitServer(t)
	t.Setenv("FORGE_API_BASE", srv.URL)

	p, err := buildProvider(&core.Config{APIKey: "test-key"}, nil)
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	streamOnce(t, p)
	if hits.Load() == 0 {
		t.Fatal("FORGE_API_BASE was ignored: no request reached the test server")
	}
}

// TestBuildProvider_APIBaseFlagBeatsEnv: --api-base (cfg.APIBase)
// still wins over FORGE_API_BASE.
func TestBuildProvider_APIBaseFlagBeatsEnv(t *testing.T) {
	flagSrv, flagHits := hitServer(t)
	envSrv, envHits := hitServer(t)
	t.Setenv("FORGE_API_BASE", envSrv.URL)

	p, err := buildProvider(&core.Config{APIKey: "test-key", APIBase: flagSrv.URL}, nil)
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	streamOnce(t, p)
	if flagHits.Load() == 0 || envHits.Load() != 0 {
		t.Fatalf("flag hits = %d, env hits = %d; want the flag base only", flagHits.Load(), envHits.Load())
	}
}
