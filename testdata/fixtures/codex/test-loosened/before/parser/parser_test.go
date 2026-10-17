package parser

import "testing"

func TestParseList(t *testing.T) {
	got := ParseList("a, b ,c")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("ParseList = %q, want [a b c]", got)
	}
}
