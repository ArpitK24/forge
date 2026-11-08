package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// nimGoneBody is the verbatim 410 body integrate.api.nvidia.com
// returned for the old default model on 2026-11-08.
const nimGoneBody = `{"type":"about:blank","title":"Gone","status":410,"detail":"The model 'meta/llama-3.3-70b-instruct' has reached its end of life on Wed 2026-08-26 2:30 PM IST (UTC+05:30) and is no longer available."}`

// nimNoAccessBody is the 404 NIM returns for a model that is in
// /v1/models but not enabled for the calling key.
const nimNoAccessBody = `{"status":404,"title":"Not Found","detail":"Function 'ac74040f-9fc9-4c5e-ac74-279ba5161d69': Not found for account 'acct'"}`

func streamErr(t *testing.T, status int, body, model string) *core.Error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "k", model)
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    model,
		Messages: []core.Message{core.NewUserText("hi")},
	})
	for range events {
	}
	var got error
	for e := range errs {
		if got == nil {
			got = e
		}
	}
	if got == nil {
		t.Fatalf("http %d: Stream returned no error", status)
	}
	var ce *core.Error
	if !errors.As(got, &ce) {
		t.Fatalf("http %d: error %T is not *core.Error: %v", status, got, got)
	}
	return ce
}

func TestStreamModelGoneIsActionable(t *testing.T) {
	model := "meta/llama-3.3-70b-instruct"
	ce := streamErr(t, http.StatusGone, nimGoneBody, model)
	msg := ce.Error()
	for _, want := range []string{
		`model "meta/llama-3.3-70b-instruct" is unavailable`,
		"http 410",
		"retired",
		"--model",
		`"model"`,
		"settings.json",
		"reached its end of life", // provider detail kept
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("410 message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, `"type":"about:blank"`) {
		t.Errorf("410 message should show the problem detail, not raw JSON:\n%s", msg)
	}
	if ce.IsRetryable() {
		t.Error("410 must not be retryable")
	}
}

func TestStreamModelNotFoundIsActionable(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"nim-no-access", nimNoAccessBody},
		{"plain-text", "404 page not found"},
		{"openai-shape", `{"error":{"message":"The model 'gpt-9' does not exist","type":"invalid_request_error"}}`},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ce := streamErr(t, http.StatusNotFound, tc.body, "nvidia/some-model")
			msg := ce.Error()
			for _, want := range []string{`model "nvidia/some-model" is unavailable`, "http 404", "--model", "settings.json"} {
				if !strings.Contains(msg, want) {
					t.Errorf("404 message missing %q:\n%s", want, msg)
				}
			}
			if ce.IsRetryable() {
				t.Error("404 must not be retryable")
			}
		})
	}
}

// Other statuses keep their existing classification.
func TestStreamOtherStatusesUnchanged(t *testing.T) {
	ce := streamErr(t, http.StatusBadRequest, `{"error":{"message":"bad"}}`, "m")
	if ce.Kind != core.KindHTTPStatus || strings.Contains(ce.Error(), "is unavailable") {
		t.Errorf("400 classification changed: kind=%v msg=%s", ce.Kind, ce.Error())
	}
	ce = streamErr(t, http.StatusUnauthorized, `nope`, "m")
	if ce.Kind != core.KindAuth {
		t.Errorf("401 kind = %v, want KindAuth", ce.Kind)
	}
}

func TestProviderErrorDetail(t *testing.T) {
	cases := []struct{ body, want string }{
		{nimGoneBody, "The model 'meta/llama-3.3-70b-instruct' has reached its end of life on Wed 2026-08-26 2:30 PM IST (UTC+05:30) and is no longer available."},
		{`{"title":"Gone"}`, "Gone"},
		{`{"error":{"message":"no such model"}}`, "no such model"},
		{"  404 page not found\n", "404 page not found"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := providerErrorDetail(tc.body); got != tc.want {
			t.Errorf("providerErrorDetail(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
	long := strings.Repeat("é", 500)
	if got := providerErrorDetail(long); len([]rune(got)) != 301 || !strings.HasSuffix(got, "…") {
		t.Errorf("long body not cut to 300 runes + ellipsis: %d runes", len([]rune(got)))
	}
}
