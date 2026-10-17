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

func TestIsCIPath(t *testing.T) {
	for p, want := range map[string]bool{
		".github/workflows/ci.yml": true, `sub\.circleci\config.yml`: true, ".gitlab-ci.yml": true,
		"Jenkinsfile": true, "build/azure-pipelines.yml": true,
		"a_test.go": false, "docs/workflows.md": false, "tests/ci.yml": false,
	} {
		if got := IsCIPath(p); got != want {
			t.Errorf("IsCIPath(%q) = %v, want %v", p, got, want)
		}
	}
}
