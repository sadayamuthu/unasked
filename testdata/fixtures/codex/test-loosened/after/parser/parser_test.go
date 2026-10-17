package parser

import "testing"

func TestParseList(t *testing.T) {
	got := ParseList("a, b ,c")
	if len(got) < 3 {
		t.Fatalf("ParseList = %q, want [a b c]", got)
	}
}
