// Package editutil is a tiny helper package used by the
// future Edit tool (Phase 2.1 / Phase 3) to count occurrences
// of a needle in a haystack — the disambiguation step the spec
// requires in §3.2: "Counts occurrences of old_string; if
// replace_all is false and count > 1, error out as ambiguous."
//
// We ship it in Phase 2 so the Phase 2.1 / Phase 3 Edit tool
// lands with a one-line change to the file. The package is
// intentionally minimal: one function, one file, no deps.
package editutil

// CountOccurrences returns the number of non-overlapping
// occurrences of needle in haystack. An empty needle is
// treated as "zero matches" — a common convention that
// matches the typical "search for nothing" being undefined.
//
// Behavior:
//
//   - Empty needle → 0 (avoids the "every position is a match"
//     degenerate case that an "all positions match" definition
//     would produce).
//   - Empty haystack → 0.
//   - Non-overlapping: from the end of each match, we continue
//     searching forward. So "aaaa" in "aaaaaaaa" returns 2,
//     not 4 or 8.
//   - Byte-level, not rune-level. The Edit tool's old_string
//     always comes from the model against text we wrote, so
//     a byte match is the right thing.
func CountOccurrences(haystack, needle string) int {
	if needle == "" || haystack == "" {
		return 0
	}
	if len(needle) > len(haystack) {
		return 0
	}
	count := 0
	for i := 0; i+len(needle) <= len(haystack); {
		if haystack[i:i+len(needle)] == needle {
			count++
			i += len(needle)
			continue
		}
		i++
	}
	return count
}
