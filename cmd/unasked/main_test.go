package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"version"}, t.TempDir(), strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "unasked dev") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"bogus"}, t.TempDir(), strings.NewReader(""), &out, &errb)
	if code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
}
