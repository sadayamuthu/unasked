package hook

import (
	"path/filepath"
	"strings"
)

// Claude Code hooks, verified 2026-10-16 against
// https://code.claude.com/docs/en/hooks:
//   - settings.json "hooks" → event → [{matcher?, hooks: [{type: "command",
//     command, timeout (seconds)}]}]; Stop and UserPromptSubmit take no
//     matcher, and an omitted SessionStart matcher matches every source.
//   - stdin payload: session_id, transcript_path, cwd, hook_event_name, and
//     on Stop stop_hook_active ("true when Claude Code is already continuing
//     as a result of a stop hook").
//   - a Stop hook that exits 0 with {"decision":"block","reason":"…"} on
//     stdout keeps Claude going with reason as its instruction.
//   - subagent transcripts live in <transcript without .jsonl>/subagents/.

// claudeEvents are the events unasked hooks: the first two record the
// session-start baseline, Stop captures and reports.
var claudeEvents = []string{"SessionStart", "UserPromptSubmit", "Stop"}

// ClaudeCode is the Claude Code hook spec. User scope is
// ~/.claude/settings.json; project scope is <repo>/.claude/settings.json,
// which is shared through the repository.
var ClaudeCode = Spec{
	Agent: "claude-code",
	ConfigPath: func(home, repo string, project bool) string {
		if project {
			return filepath.Join(repo, ".claude", "settings.json")
		}
		return filepath.Join(home, ".claude", "settings.json")
	},
	Install: func(cfg map[string]any, bin string) (map[string]any, bool) {
		return installEvents(cfg, claudeEvents, Command(bin, "claude-code"))
	},
	Uninstall:    uninstallAll,
	ParsePayload: parseCommon,
	Respond:      blockResponse,
	Extra: func(p Payload) []string {
		if p.TranscriptPath == "" {
			return nil
		}
		return []string{filepath.Join(strings.TrimSuffix(p.TranscriptPath, filepath.Ext(p.TranscriptPath)), "subagents")}
	},
}
