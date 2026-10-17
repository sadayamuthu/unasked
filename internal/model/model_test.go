package model

import "testing"

func TestHighRisk(t *testing.T) {
	for l, want := range map[Label]bool{
		"deletion": true, "test": true, "dependency": true,
		"feature": false, "refactor": false, "collateral": false, "unknown": false,
	} {
		if got := l.HighRisk(); got != want {
			t.Errorf("%s.HighRisk() = %v, want %v", l, got, want)
		}
	}
}
