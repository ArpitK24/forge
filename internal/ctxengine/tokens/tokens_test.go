package tokens

import "testing"

func TestEstimate(t *testing.T) {
	for s, want := range map[string]int{"": 0, "a": 1, "abcd": 1, "abcde": 2, "│": 1} {
		if got := Estimate(s); got != want {
			t.Errorf("Estimate(%q) = %d, want %d", s, got, want)
		}
		if got := EstimateBytes(len(s)); got != want {
			t.Errorf("EstimateBytes(%d) = %d, want %d", len(s), got, want)
		}
	}
}
