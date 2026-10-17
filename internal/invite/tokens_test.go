package invite

import (
	"reflect"
	"testing"

	"github.com/sadayamuthu/unasked/internal/model"
)

func TestTokensCamelSnake(t *testing.T) {
	got := Tokens("parseDuration rate_limit HTTPServer")
	want := []string{"pars", "durat", "rate", "limit", "http", "server"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tokens() = %v, want %v", got, want)
	}
}

func TestTokensDropStopAndSingle(t *testing.T) {
	got := Tokens("Return the x and a func parsing with nil header")
	want := []string{"pars", "header"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tokens() = %v, want %v", got, want)
	}
}

func TestTokensKeepsDuplicatesInOrder(t *testing.T) {
	got := Tokens("header Header header")
	want := []string{"header", "header", "header"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tokens() = %v, want %v", got, want)
	}
}

func TestHunkTokensIncludesPath(t *testing.T) {
	h := model.Hunk{File: "internal/http/headers.go"}
	got := HunkTokens(h)
	for _, want := range []string{"intern", "http", "header"} {
		if !contains(got, want) {
			t.Errorf("HunkTokens() = %v, missing %q", got, want)
		}
	}
	if contains(got, "go") {
		t.Errorf("HunkTokens() = %v, should not include the file extension", got)
	}
}

func TestHunkTokensDotfileKeepsName(t *testing.T) {
	got := HunkTokens(model.Hunk{File: ".gitignore"})
	if !contains(got, "gitignor") {
		t.Errorf("HunkTokens() = %v, want dotfile name kept", got)
	}
}

func TestHunkTokensSources(t *testing.T) {
	h := model.Hunk{
		File:    "pkg/cache.go",
		Added:   []string{"cacheEntry := newEntry()"},
		Removed: []string{"legacyStore.Flush()"},
		Symbols: model.SymbolDelta{Enclosing: "evictExpired"},
	}
	got := HunkTokens(h)
	for _, want := range []string{"cach", "entri", "legaci", "store", "flush", "evict", "expir", "pkg"} {
		if !contains(got, want) {
			t.Errorf("HunkTokens() = %v, missing %q", got, want)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
