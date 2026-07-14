package editutil

import "testing"

func TestCountOccurrencesEmpty(t *testing.T) {
	if got := CountOccurrences("", "a"); got != 0 {
		t.Errorf("empty haystack = %d, want 0", got)
	}
	if got := CountOccurrences("abc", ""); got != 0 {
		t.Errorf("empty needle = %d, want 0", got)
	}
	if got := CountOccurrences("", ""); got != 0 {
		t.Errorf("both empty = %d, want 0", got)
	}
}

func TestCountOccurrencesZero(t *testing.T) {
	if got := CountOccurrences("abc", "z"); got != 0 {
		t.Errorf("no match = %d, want 0", got)
	}
	if got := CountOccurrences("abc", "abcd"); got != 0 {
		t.Errorf("needle longer than haystack = %d, want 0", got)
	}
}

func TestCountOccurrencesSingle(t *testing.T) {
	if got := CountOccurrences("hello world", "world"); got != 1 {
		t.Errorf("one match = %d, want 1", got)
	}
	if got := CountOccurrences("hello world", "hello"); got != 1 {
		t.Errorf("match at start = %d, want 1", got)
	}
}

func TestCountOccurrencesMultiple(t *testing.T) {
	if got := CountOccurrences("aaaa", "aa"); got != 2 {
		t.Errorf("aaaa/aa = %d, want 2 (non-overlapping)", got)
	}
	if got := CountOccurrences("ababab", "ab"); got != 3 {
		t.Errorf("ababab/ab = %d, want 3", got)
	}
	if got := CountOccurrences("the cat sat on the mat", "at"); got != 3 {
		t.Errorf("3 'at' in sentence = %d, want 3", got)
	}
}

func TestCountOccurrencesNonOverlapping(t *testing.T) {
	// Spec §3.2: "if replace_all is false and count > 1, error
	// out as ambiguous (force the caller to disambiguate)."
	// The "count" here is the non-overlapping count: the Edit
	// tool wants to know how many distinct *replacement
	// positions* there are. Non-overlapping is the right
	// definition.
	got := CountOccurrences("aaaa", "aa")
	if got != 2 {
		t.Errorf("non-overlapping count of 'aa' in 'aaaa' = %d, want 2", got)
	}
}

func TestCountOccurrencesCaseSensitive(t *testing.T) {
	// Spec is silent on case sensitivity, and the Edit tool
	// matches byte-for-byte. We don't case-fold.
	if got := CountOccurrences("Abc abc", "abc"); got != 1 {
		t.Errorf("case-sensitive = %d, want 1", got)
	}
}
