package api

import (
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// TestMergeWithKnown_MatchesCuratedEntry asserts that a listed
// model whose id matches a knownModels entry inherits the
// curated entry's capability flags and ContextWindow. The ID
// and Provider come from the listing and are preserved
// unchanged.
func TestMergeWithKnown_MatchesCuratedEntry(t *testing.T) {
	// Anthropic's live listing is expected to return the id
	// "claude-sonnet-4-5"; knownModels has a curated entry
	// that sets SupportsPromptCaching + SupportsExtendedThinking
	// and a 200k context window.
	listed := []ModelInfo{
		{ID: "claude-sonnet-4-5", Provider: core.ProviderAnthropic},
	}
	out := MergeWithKnown(listed)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	got := out[0]
	if got.ID != "claude-sonnet-4-5" {
		t.Errorf("ID = %q, want 'claude-sonnet-4-5' (must come from listing)", got.ID)
	}
	if got.Provider != core.ProviderAnthropic {
		t.Errorf("Provider = %q, want %q (must come from listing)", got.Provider, core.ProviderAnthropic)
	}
	if !got.SupportsPromptCaching {
		t.Errorf("SupportsPromptCaching = false, want true (from knownModels)")
	}
	if !got.SupportsExtendedThinking {
		t.Errorf("SupportsExtendedThinking = false, want true (from knownModels)")
	}
	if got.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200000 (from knownModels)", got.ContextWindow)
	}
}

// TestMergeWithKnown_UnmatchedKeepsZeroFlags is the regression
// guard for the whole point of step 3: a live-listed model whose
// id doesn't match any curated entry keeps zero-value capability
// flags. The old LookupModel path would have set these from a
// substring heuristic (e.g. "contains 'claude' → assume cache +
// thinking on"). MergeWithKnown does NOT.
func TestMergeWithKnown_UnmatchedKeepsZeroFlags(t *testing.T) {
	listed := []ModelInfo{
		// Hypothetical Claude variant Forge hasn't curated
		// yet — Anthropic may have shipped it after our last
		// knownModels update. Substring "claude" matches.
		{ID: "claude-future-9-ultra", Provider: core.ProviderAnthropic},
		// Unknown non-Claude model — substring "claude" does
		// not match. Listed-only with no curated data.
		{ID: "some-experimental-model", Provider: core.ProviderOpenAI},
	}
	out := MergeWithKnown(listed)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}

	// The "claude-future-9-ultra" entry must NOT inherit
	// caching/thinking just because its id contains "claude".
	// The old LookupModel heuristic would have set both true;
	// MergeWithKnown leaves them false until a curated entry
	// or a live /v1/models response confirms them.
	if out[0].SupportsPromptCaching {
		t.Errorf("unmatched 'claude-future-9-ultra' got SupportsPromptCaching=true from id heuristic; want false")
	}
	if out[0].SupportsExtendedThinking {
		t.Errorf("unmatched 'claude-future-9-ultra' got SupportsExtendedThinking=true from id heuristic; want false")
	}

	// Non-claude model — same expectation, no flags inferred.
	if out[1].SupportsPromptCaching || out[1].SupportsExtendedThinking {
		t.Errorf("unmatched 'some-experimental-model' got capability flags from heuristic; want false")
	}
}

// TestMergeWithKnown_FillsContextWindowFromHeuristic verifies
// the one place MergeWithKnown still uses a heuristic: context
// window for unmatched models. ContextWindowForModel returns
// 128k for unknown current-gen ids and 8k for old-gen ones;
// this test covers both.
func TestMergeWithKnown_FillsContextWindowFromHeuristic(t *testing.T) {
	cases := []struct {
		id           string
		wantWindow   int
		wantProvider string
	}{
		// Unknown but plausibly current-gen id — falls into the
		// 128k default branch.
		{"some-brand-new-model-2026", 128_000, core.ProviderOpenAI},
		// Old-gen substring ("gpt-3.5") triggers the
		// conservative 8k fallback.
		{"gpt-3.5-turbo-clone", 8_000, core.ProviderOpenAI},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			out := MergeWithKnown([]ModelInfo{
				{ID: tc.id, Provider: tc.wantProvider},
			})
			if out[0].ContextWindow != tc.wantWindow {
				t.Errorf("ContextWindow = %d, want %d", out[0].ContextWindow, tc.wantWindow)
			}
			// Capability flags still must be zero — the
			// heuristic applies to context window ONLY.
			if out[0].SupportsPromptCaching || out[0].SupportsExtendedThinking {
				t.Errorf("capability flags set by heuristic on %q; want false", tc.id)
			}
		})
	}
}

// TestMergeWithKnown_ListingProvidedContextWindowWins ensures
// that if the live listing already gave us a context window
// (a future OpenRouter-style adapter, say, which embeds
// context-window data), MergeWithKnown doesn't override it
// with the heuristic fallback.
func TestMergeWithKnown_ListingProvidedContextWindowWins(t *testing.T) {
	listed := []ModelInfo{
		{ID: "totally-new-model", Provider: core.ProviderOpenAI, ContextWindow: 32_000},
	}
	out := MergeWithKnown(listed)
	if out[0].ContextWindow != 32_000 {
		t.Errorf("ContextWindow = %d, want 32000 (from listing, not heuristic)", out[0].ContextWindow)
	}
}

// TestLookupAndMergeAgreeOnMatchedCuratedEntry pins the
// contract that LookupModel and MergeWithKnown agree on the
// matched/unmatched verdict for the same input id. The two
// functions are independently written and are ALLOWED to
// differ on capability-flag values for unmatched ids (by
// design — MergeWithKnown refuses to guess) — but they MUST
// agree on which ids are catalog hits. Otherwise an adapter
// that calls LookupModel at construction and the listing
// path that calls MergeWithKnown would disagree on which
// models even have curated metadata.
//
// Concretely, the contract is:
//
//   - For a catalog id `m.ID` (or any input id that
//     substring-matches a catalog id), LookupModel returns
//     the catalog row verbatim — `ID == m.ID` and
//     `Provider == m.Provider`.
//   - For the same input id, MergeWithKnown inherits the
//     catalog's `SupportsPromptCaching` and
//     `SupportsExtendedThinking` flags (ContextWindow too —
//     already covered by other tests).
//
// We pin both directions in one test: for every knownModels
// entry, drive both functions with the exact id and assert
// the same catalog-row origin.
//
// We also drive both with a curated substring variant
// (catalog id with a date suffix) and a small unmatched
// corpus, and assert the matching functions stay consistent.
//
// If this test fails, one of the two substring-match loops
// has diverged — fix by routing through the shared
// findKnownModel helper rather than copy-editing both.
func TestLookupAndMergeAgreeOnMatchedCuratedEntry(t *testing.T) {
	// 1) Every knownModels id, exact-match: both functions
	// must return / inherit the catalog row. Note that the
	// substring matcher is symmetric, so an exact-id lookup
	// for a SHORT id (e.g. "claude-opus-4") can hit a LONGER
	// catalog entry first if iteration order puts it ahead
	// (e.g. "claude-opus-4-1"). What the agreement contract
	// pins here is that BOTH functions agree on which row is
	// returned — they share the matcher via findKnownModel.
	for _, m := range knownModels {
		t.Run("exact:"+m.ID, func(t *testing.T) {
			lookup := LookupModel(m.ID)
			merged := MergeWithKnown([]ModelInfo{{ID: m.ID, Provider: m.Provider}})

			// The matched catalog row (the winner of the
			// substring loop). Both functions must agree on
			// it — that's the whole point of the agreement
			// test.
			winner, ok := findKnownModel(m.ID)
			if !ok {
				t.Fatalf("findKnownModel(%q) returned ok=false; catalog iteration regression", m.ID)
			}

			// LookupModel: must return the catalog row
			// verbatim.
			if lookup.ID != winner.ID {
				t.Errorf("LookupModel(%q).ID = %q; want %q (catalog row from winner)",
					m.ID, lookup.ID, winner.ID)
			}
			if lookup.Provider != winner.Provider {
				t.Errorf("LookupModel(%q).Provider = %q; want %q (catalog row from winner)",
					m.ID, lookup.Provider, winner.Provider)
			}
			if lookup.SupportsPromptCaching != winner.SupportsPromptCaching {
				t.Errorf("LookupModel(%q).SupportsPromptCaching = %v; want %v (from catalog row)",
					m.ID, lookup.SupportsPromptCaching, winner.SupportsPromptCaching)
			}
			if lookup.SupportsExtendedThinking != winner.SupportsExtendedThinking {
				t.Errorf("LookupModel(%q).SupportsExtendedThinking = %v; want %v (from catalog row)",
					m.ID, lookup.SupportsExtendedThinking, winner.SupportsExtendedThinking)
			}

			// MergeWithKnown: must inherit the catalog
			// row's capability flags (the documented
			// behavior of the overlay).
			if merged[0].SupportsPromptCaching != winner.SupportsPromptCaching {
				t.Errorf("MergeWithKnown[%q].SupportsPromptCaching = %v; want %v (from catalog row)",
					m.ID, merged[0].SupportsPromptCaching, winner.SupportsPromptCaching)
			}
			if merged[0].SupportsExtendedThinking != winner.SupportsExtendedThinking {
				t.Errorf("MergeWithKnown[%q].SupportsExtendedThinking = %v; want %v",
					m.ID, merged[0].SupportsExtendedThinking, winner.SupportsExtendedThinking)
			}
		})
	}

	// 2) Substring variants — catalog id with a date or
	// revision suffix. The substring matcher is supposed to
	// catch these. Exercise both functions with a contrived
	// subset to keep the test cheap; full coverage lives
	// behind the loop above.
	type subCase struct {
		input   string
		catalog string
	}
	substringCases := []subCase{
		{"claude-opus-4-1-20251001", "claude-opus-4-1"},
		{"claude-sonnet-4-5-20251001", "claude-sonnet-4-5"},
	}
	for _, tc := range substringCases {
		t.Run("substring:"+tc.input, func(t *testing.T) {
			// LookupModel: on a substring hit, the old
			// inlined loop returned `m` (the catalog row),
			// not the caller-supplied id. The consolidated
			// findKnownModel does the same. Pin that
			// behavior — ID of returned ModelInfo must
			// match the catalog row, NOT the caller input.
			lookup := LookupModel(tc.input)
			if lookup.ID != tc.catalog {
				t.Errorf("LookupModel(%q).ID = %q; want %q (catalog row expected on substring hit)",
					tc.input, lookup.ID, tc.catalog)
			}

			// MergeWithKnown: substring match is what
			// populates the curated flags; verify the
			// substring does hit by checking
			// SupportsPromptCaching matches the catalog
			// row's. (If the matcher were broken, the
			// value would be false — the zero default for
			// unmatched.)
			cat, ok := findKnownModel(tc.input)
			if !ok {
				t.Fatalf("findKnownModel(%q) returned ok=false; substring matcher regression", tc.input)
			}
			merged := MergeWithKnown([]ModelInfo{{ID: tc.input, Provider: cat.Provider}})
			if merged[0].SupportsPromptCaching != cat.SupportsPromptCaching {
				t.Errorf("MergeWithKnown[%q].SupportsPromptCaching = %v; want %v",
					tc.input, merged[0].SupportsPromptCaching, cat.SupportsPromptCaching)
			}
		})
	}

	// 3) Unmatched corpus. Both functions must agree these
	// are NOT catalog hits. The disagreement on the flag
	// values (LookupModel synthesizes heuristics,
	// MergeWithKnown refuses to guess) is by design and
	// tested separately — here we only pin the
	// matched/unmatched verdict.
	//
	// Discriminator: LookupModel's ID on a synthesized
	// entry equals the caller-supplied id, whereas a
	// catalog hit's ID equals the catalog's `m.ID`.
	unmatchedCorpus := []string{
		"claude-future-9-ultra", // contains "claude", no curated hit
		"some-experimental-model",
		"gpt-3.5-turbo-clone", // old-gen substring, no curated hit
	}
	for _, id := range unmatchedCorpus {
		t.Run("unmatched:"+id, func(t *testing.T) {
			lookup := LookupModel(id)
			if lookup.ID != id {
				t.Errorf("LookupModel(%q).ID = %q; want %q (synthesized branch expected; catalog hit would return a different ID)",
					id, lookup.ID, id)
			}

			merged := MergeWithKnown([]ModelInfo{{ID: id, Provider: core.ProviderOpenAI}})
			// An unmatched listing result keeps zero flags.
			if merged[0].SupportsPromptCaching || merged[0].SupportsExtendedThinking {
				t.Errorf("MergeWithKnown(%q) set capability flags on unmatched id; overlay must refuse to guess",
					id)
			}
			// ID is preserved from the listing.
			if merged[0].ID != id {
				t.Errorf("MergeWithKnown(%q).ID = %q; want %q (input preserved)",
					id, merged[0].ID, id)
			}
		})
	}
}

// TestMergeWithKnown_DoesNotMutateInput confirms the function
// returns a new slice; the caller's input is left alone. This
// matters because adapters may keep the raw listing around for
// debugging or for a second pass (e.g. /models command output).
func TestMergeWithKnown_DoesNotMutateInput(t *testing.T) {
	listed := []ModelInfo{
		{ID: "claude-sonnet-4-5", Provider: core.ProviderAnthropic},
	}
	snapshot := listed[0] // shallow copy of the value
	_ = MergeWithKnown(listed)

	if listed[0] != snapshot {
		t.Errorf("MergeWithKnown mutated its input: before=%+v after=%+v", snapshot, listed[0])
	}
}
