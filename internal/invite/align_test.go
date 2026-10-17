package invite

import (
	"strings"
	"testing"

	"github.com/sadayamuthu/unasked/internal/model"
)

func TestAlignExactThenLCS(t *testing.T) {
	added := seq("line ", 20)
	h := model.Hunk{File: "a.go", Added: added}
	exact := model.EditEvent{Turn: 1, File: "a.go", After: "pre\n" + strings.Join(added, "\n") + "\npost"}
	// 17 of 20 lines in order, interleaved with other lines: LCS 0.85.
	var near []string
	for i, l := range added {
		if i%7 == 3 { // drops i=3,10,17
			near = append(near, "changed")
			continue
		}
		near = append(near, l)
	}
	lcs85 := model.EditEvent{Turn: 2, File: "a.go", After: strings.Join(near, "\n")}
	lcs70 := model.EditEvent{Turn: 2, File: "a.go", After: strings.Join(added[:14], "\n")}

	if got := Align(h, []model.EditEvent{exact, lcs85}); got == nil || got.Turn != 1 {
		t.Errorf("exact should beat a later LCS match, got %+v", got)
	}
	if got := Align(h, []model.EditEvent{lcs85}); got == nil || got.Turn != 2 {
		t.Errorf("85%% LCS should align, got %+v", got)
	}
	if got := Align(h, []model.EditEvent{lcs70}); got != nil {
		t.Errorf("70%% LCS should not align, got %+v", got)
	}
	other := lcs85
	other.File = "b.go"
	if got := Align(h, []model.EditEvent{other}); got != nil {
		t.Errorf("LCS match in another file should not align, got %+v", got)
	}
}

func TestAlignLatestTurnFirst(t *testing.T) {
	h := model.Hunk{File: "a.go", Added: []string{"x := 1"}}
	evs := []model.EditEvent{
		{Turn: 5, File: "a.go", After: "x := 1", Statement: "late"},
		{Turn: 2, File: "a.go", After: "x := 1", Statement: "early"},
		{Turn: 5, File: "a.go", After: "x := 1\n", Statement: "late, second"},
	}
	if got := Align(h, evs); got == nil || got.Statement != "late, second" {
		t.Errorf("got %+v, want the last event of the latest turn", got)
	}
}

func TestAlignExactNeedsSameFile(t *testing.T) {
	h := model.Hunk{File: "a.go", Added: []string{"}"}}
	if got := Align(h, []model.EditEvent{{Turn: 1, File: "b.go", After: "}"}}); got != nil {
		t.Errorf("exact match in another file aligned: %+v", got)
	}
}

func TestAlignPureDeletion(t *testing.T) {
	h := model.Hunk{File: "a.go", Removed: []string{"old()"}}
	evs := []model.EditEvent{{Turn: 1, File: "a.go", After: "x"}, {Turn: 3, File: "a.go", After: ""}, {Turn: 4, File: "b.go"}}
	if got := Align(h, evs); got == nil || got.Turn != 3 {
		t.Errorf("deletion should align to latest same-file event, got %+v", got)
	}
	if got := Align(h, evs[2:]); got != nil {
		t.Errorf("deletion aligned to another file: %+v", got)
	}
}

func TestAlignCRLF(t *testing.T) {
	h := model.Hunk{File: "a.go", Added: []string{"a()", "b()"}}
	if got := Align(h, []model.EditEvent{{Turn: 1, File: "a.go", After: "a()\r\nb()\r\n"}}); got == nil {
		t.Error("CRLF After should align")
	}
}

// Claude Code's Edit tool replaces any unique substring, so new_string is
// often part of a line ("users.FetchUser(ctx") or a line without its
// indentation. Such an edit aligns to the hunk whose added text contains it.
func TestAlignPartialLineEdit(t *testing.T) {
	h := model.Hunk{File: "a.go", Added: []string{"\tu, err := users.FetchUser(ctx, id)"}}
	frag := model.EditEvent{Turn: 1, File: "a.go", After: "users.FetchUser(ctx", Statement: "frag"}
	if got := Align(h, []model.EditEvent{frag}); got == nil || got.Statement != "frag" {
		t.Errorf("fragment edit did not align: %+v", got)
	}
	unindented := model.EditEvent{Turn: 1, File: "a.go", After: "u, err := users.FetchUser(ctx, id)\n", Statement: "noindent"}
	if got := Align(h, []model.EditEvent{unindented}); got == nil || got.Statement != "noindent" {
		t.Errorf("unindented edit did not align: %+v", got)
	}
	// Exact containment still wins over a fragment.
	whole := model.EditEvent{Turn: 1, File: "a.go", After: "\tu, err := users.FetchUser(ctx, id)", Statement: "whole"}
	if got := Align(h, []model.EditEvent{whole, frag}); got == nil || got.Statement != "whole" {
		t.Errorf("fragment beat an exact match: %+v", got)
	}
	// A multi-line edit that ends mid-line covers the line it starts.
	logLine := model.Hunk{File: "a.go", Added: []string{"\t\tlog.Printf(\"%s\", start.Format(time.RFC3339Nano), r.Method)"}}
	multi := model.EditEvent{Turn: 1, File: "a.go", After: "start := time.Now().UTC()\n\t\tnext()\n\t\tlog.Printf(\"%s\", start.Format(time.RFC3339Nano),", Statement: "multi"}
	if got := Align(logLine, []model.EditEvent{multi}); got == nil || got.Statement != "multi" {
		t.Errorf("edit ending mid-line did not align: %+v", got)
	}
	// ...but not a line it does not reach.
	other := model.Hunk{File: "a.go", Added: []string{"\treturn nil"}}
	if got := Align(other, []model.EditEvent{multi}); got != nil {
		t.Errorf("edit aligned to a line it does not cover: %+v", got)
	}
	// A fragment without a content token ("}", "(") aligns nothing, nor does
	// one from another file or one the hunk does not contain.
	for _, ev := range []model.EditEvent{
		{Turn: 1, File: "a.go", After: ")"},
		{Turn: 1, File: "b.go", After: "users.FetchUser(ctx"},
		{Turn: 1, File: "a.go", After: "users.GetUser(ctx"},
	} {
		if got := Align(h, []model.EditEvent{ev}); got != nil {
			t.Errorf("%+v aligned", ev)
		}
	}
}
