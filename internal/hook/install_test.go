package hook

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

const testBin = "/opt/bin/unasked"

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := readFile(t, p)
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
		t.Errorf("%s mismatch:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// seed copies a fixture to the agent's user config path under a fresh home.
func seed(t *testing.T, s Spec, fixture string, mode os.FileMode) (home, path string) {
	t.Helper()
	t.Setenv("CODEX_HOME", "")
	home = t.TempDir()
	path = s.ConfigPath(home, "", false)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if fixture != "" {
		if err := os.WriteFile(path, readFile(t, filepath.Join("testdata", fixture)), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	return home, path
}

func TestInstallMergesExistingSettings(t *testing.T) {
	home, path := seed(t, ClaudeCode, "claude-existing.json", 0o600)
	got, changed, err := Install(ClaudeCode, home, "", false, testBin)
	if err != nil || !changed || got != path {
		t.Fatalf("Install = %q, %v, %v; want %q, true, nil", got, changed, err, path)
	}
	golden(t, "claude-installed.golden.json", readFile(t, path))
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600 kept", fi.Mode().Perm())
		}
	}
	if ok, p := Status(ClaudeCode, home, ""); !ok || p != path {
		t.Errorf("Status = %v, %q", ok, p)
	}
}

func TestInstallCreatesMissingFile(t *testing.T) {
	home, path := seed(t, ClaudeCode, "", 0)
	if _, changed, err := Install(ClaudeCode, home, "", false, testBin); err != nil || !changed {
		t.Fatalf("Install: %v, %v", changed, err)
	}
	if !strings.Contains(string(readFile(t, path)), "/opt/bin/unasked hook run --agent claude-code") {
		t.Errorf("hook command missing:\n%s", readFile(t, path))
	}
	if _, err := os.Stat(path + backupSuffix); !os.IsNotExist(err) {
		t.Errorf("backup of a file that did not exist: %v", err)
	}
}

func TestInstallIdempotent(t *testing.T) {
	home, path := seed(t, ClaudeCode, "claude-existing.json", 0o644)
	if _, _, err := Install(ClaudeCode, home, "", false, testBin); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, path)
	if _, changed, err := Install(ClaudeCode, home, "", false, testBin); err != nil || changed {
		t.Fatalf("second install changed=%v err=%v", changed, err)
	}
	if !bytes.Equal(first, readFile(t, path)) {
		t.Error("second install rewrote the file")
	}
	// A new binary location replaces our entries rather than duplicating them.
	if _, changed, err := Install(ClaudeCode, home, "", false, "/usr/local/bin/unasked"); err != nil || !changed {
		t.Fatalf("reinstall from new path changed=%v err=%v", changed, err)
	}
	out := string(readFile(t, path))
	if n := strings.Count(out, marker); n != 3 {
		t.Errorf("%d entries with marker, want 3 (one per event):\n%s", n, out)
	}
	if strings.Contains(out, testBin) {
		t.Error("old binary path left behind")
	}
}

func TestInstallBacksUpOnce(t *testing.T) {
	home, path := seed(t, ClaudeCode, "claude-existing.json", 0o600)
	orig := readFile(t, path)
	if _, _, err := Install(ClaudeCode, home, "", false, testBin); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, path+backupSuffix), orig) {
		t.Fatal("backup differs from the original")
	}
	if _, _, err := Uninstall(ClaudeCode, home, "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(ClaudeCode, home, "", false, "/other/unasked"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, path+backupSuffix), orig) {
		t.Error("backup overwritten by a later install")
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	home, path := seed(t, ClaudeCode, "claude-existing.json", 0o600)
	if _, _, err := Install(ClaudeCode, home, "", false, testBin); err != nil {
		t.Fatal(err)
	}
	got, changed, err := Uninstall(ClaudeCode, home, "", false)
	if err != nil || !changed || got != path {
		t.Fatalf("Uninstall = %q, %v, %v", got, changed, err)
	}
	// Back to the original document (same keys, same order, same values).
	golden(t, "claude-existing.json", readFile(t, path))
	if ok, _ := Status(ClaudeCode, home, ""); ok {
		t.Error("Status still installed")
	}
	if _, changed, err := Uninstall(ClaudeCode, home, "", false); err != nil || changed {
		t.Errorf("second uninstall changed=%v err=%v", changed, err)
	}
}

func TestUninstallMissingFile(t *testing.T) {
	home, path := seed(t, ClaudeCode, "", 0)
	if _, changed, err := Uninstall(ClaudeCode, home, "", false); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("uninstall created the file")
	}
}

func TestInstallRefusesBadConfig(t *testing.T) {
	for name, body := range map[string]string{
		"invalid":         `{"hooks": [`,
		"not object":      `[1, 2]`,
		"hooks not obj":   `{"hooks": "x"}`,
		"event not array": `{"hooks": {"Stop": {"a": 1}}}`,
		"trailing data":   `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			home, path := seed(t, ClaudeCode, "", 0)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Install(ClaudeCode, home, "", false, testBin); err == nil {
				t.Fatal("no error")
			}
			if string(readFile(t, path)) != body {
				t.Error("file changed")
			}
			if _, err := os.Stat(path + backupSuffix); !os.IsNotExist(err) {
				t.Error("backup written on refusal")
			}
		})
	}
}

func TestInstallFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	home, path := seed(t, ClaudeCode, "", 0)
	target := filepath.Join(t.TempDir(), "dotfiles-settings.json")
	if err := os.WriteFile(target, []byte(`{"model": "opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(ClaudeCode, home, "", false, testBin); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	if !strings.Contains(string(readFile(t, target)), marker) {
		t.Error("target not updated")
	}
}

func TestProjectScope(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home, repo := t.TempDir(), t.TempDir()
	// m8: the hook command holds this machine's absolute binary path, so a
	// project install goes to the uncommitted settings.local.json, never the
	// shared settings.json.
	path, _, err := Install(ClaudeCode, home, repo, true, testBin)
	if err != nil || path != filepath.Join(repo, ".claude", "settings.local.json") {
		t.Fatalf("path %q err %v", path, err)
	}
	if ok, p := Status(ClaudeCode, home, repo); !ok || p != path {
		t.Errorf("Status = %v, %q", ok, p)
	}
	// An entry an earlier version put in the shared settings.json is still
	// found by status and removed by uninstall, with the local one.
	shared := filepath.Join(repo, ".claude", "settings.json")
	if _, _, err := edit(shared, func(cfg map[string]any) (map[string]any, bool) { return ClaudeCode.Install(cfg, testBin) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Uninstall(ClaudeCode, home, repo, true); err != nil {
		t.Fatal(err)
	}
	if ok, p := Status(ClaudeCode, home, repo); ok {
		t.Errorf("still installed at %q after uninstall", p)
	}
	if _, _, err := edit(shared, func(cfg map[string]any) (map[string]any, bool) { return ClaudeCode.Install(cfg, testBin) }); err != nil {
		t.Fatal(err)
	}
	if ok, p := Status(ClaudeCode, home, repo); !ok || p != shared {
		t.Errorf("legacy shared install: Status = %v, %q", ok, p)
	}
	if p := Codex.ConfigPath(home, repo, true); p != filepath.Join(repo, ".codex", "hooks.json") {
		t.Errorf("codex project path %q", p)
	}
}

func TestCodexInstallGolden(t *testing.T) {
	home, path := seed(t, Codex, "codex-existing.json", 0o600)
	if path != filepath.Join(home, ".codex", "hooks.json") {
		t.Fatalf("path %q", path)
	}
	if _, changed, err := Install(Codex, home, "", false, testBin); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	golden(t, "codex-installed.golden.json", readFile(t, path))
	if _, changed, _ := Install(Codex, home, "", false, testBin); changed {
		t.Error("not idempotent")
	}
}

func TestCodexHomeHonoured(t *testing.T) {
	ch := t.TempDir()
	t.Setenv("CODEX_HOME", ch)
	t.Setenv("UNASKED_HOME", "")
	if p := Codex.ConfigPath(t.TempDir(), "", false); p != filepath.Join(ch, "hooks.json") {
		t.Errorf("path %q, want under CODEX_HOME", p)
	}
}

func TestCommandQuoting(t *testing.T) {
	cases := map[string]string{
		"/opt/bin/unasked":   "/opt/bin/unasked hook run --agent codex",
		"/My Tools/unasked":  "'/My Tools/unasked' hook run --agent codex",
		"/it's/unasked":      `'/it'\''s/unasked' hook run --agent codex`,
		`C:\Program Files\u`: `"C:\Program Files\u" hook run --agent codex`,
		// PowerShell does not run a quoted path without "&": a path that
		// needs no quoting is written bare (I7).
		`C:\Users\me\go\bin\unasked.exe`: `C:\Users\me\go\bin\unasked.exe hook run --agent codex`,
		`C:\a&b\unasked.exe`:             `"C:\a&b\unasked.exe" hook run --agent codex`,
	}
	for bin, want := range cases {
		windows := strings.Contains(bin, `\`)
		if got := command(bin, "codex", windows); got != want {
			t.Errorf("command(%q) = %q, want %q", bin, got, want)
		}
	}
	if w := WindowsPathWarning(`C:\Program Files\u`); w != "binary path contains spaces; install unasked to a path without spaces for Windows hooks" {
		t.Errorf("warning %q", w)
	}
	if w := WindowsPathWarning(`C:\Users\me\go\bin\unasked.exe`); w != "" {
		t.Errorf("warning for a safe path: %q", w)
	}
}

func TestInstallRefusesDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	home, path := seed(t, ClaudeCode, "", 0)
	missing := filepath.Join(t.TempDir(), "gone.json")
	if err := os.Symlink(missing, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(ClaudeCode, home, "", false, testBin); err == nil {
		t.Fatal("no error for a dangling symlink")
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("symlink target created")
	}
}

func TestInstallRefusesUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	home, path := seed(t, ClaudeCode, "claude-existing.json", 0o600)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	if _, _, err := Install(ClaudeCode, home, "", false, testBin); err == nil {
		t.Fatal("no error for an unreadable config")
	}
	if _, err := os.Stat(path + backupSuffix); !os.IsNotExist(err) {
		t.Error("backup written")
	}
	os.Chmod(path, 0o600)
	golden(t, "claude-existing.json", readFile(t, path))
	if ok, _ := Status(ClaudeCode, home, ""); ok {
		t.Error("status reports installed")
	}
}
