package hook

import (
	"os"
	"path/filepath"
)

// Codex hooks, verified 2026-10-16 against https://learn.chatgpt.com/docs/hooks:
//   - hooks.json beside each config layer: ~/.codex/hooks.json (the Codex
//     home, CODEX_HOME when set) and <repo>/.codex/hooks.json, shaped
//     {"hooks": {Event: [{matcher?, hooks: [{type: "command", command,
//     timeout (seconds)}]}]}}.
//   - events include SessionStart, UserPromptSubmit and Stop; the payload
//     carries session_id, transcript_path, cwd, hook_event_name and, on Stop,
//     stop_hook_active.
//   - a Stop hook that exits 0 with {"decision":"block","reason":"…"} makes
//     Codex continue with reason as a new prompt; exit 0 with no output lets
//     it stop.
//   - new or changed hooks run only after the user trusts them in /hooks.

var codexEvents = []string{"SessionStart", "UserPromptSubmit", "Stop"}

// getenv is the environment Codex's config location is read from; tests replace it.
var getenv = os.Getenv

// Codex is the Codex CLI hook spec.
var Codex = Spec{
	Agent: "codex",
	ConfigPath: func(home, repo string, project bool) string {
		if project {
			return filepath.Join(repo, ".codex", "hooks.json")
		}
		// UNASKED_HOME (tests, sandboxes) outranks CODEX_HOME, as in the adapter.
		if ch := getenv("CODEX_HOME"); ch != "" && getenv("UNASKED_HOME") == "" {
			return filepath.Join(ch, "hooks.json")
		}
		return filepath.Join(home, ".codex", "hooks.json")
	},
	Install: func(cfg map[string]any, bin string) (map[string]any, bool) {
		return installEvents(cfg, codexEvents, Command(bin, "codex"))
	},
	Uninstall:    uninstallAll,
	ParsePayload: parseCommon,
	Respond:      blockResponse,
	Extra:        func(Payload) []string { return nil },
	TrustNote:    "Codex: open /hooks in Codex once to trust the unasked hook.",
}
