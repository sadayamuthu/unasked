package adapter

import "testing"

func TestLastSentences(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"One. Two! Three? Four.", 3, "Two! Three? Four."},
		{"Only one", 3, "Only one"},
		{"A line\nanother line\n\nthird line", 2, "another line third line"},
		{"Version 1.2 is out. Done.", 2, "Version 1.2 is out. Done."},
		{"", 3, ""},
		{"  \n ", 3, ""},
		{"One. Two.", 0, ""},
	} {
		if got := LastSentences(c.in, c.n); got != c.want {
			t.Errorf("LastSentences(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
