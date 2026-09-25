#!/usr/bin/env bash
# Walk a fake session through every screen, as Claude Code's hooks would.
# Usage: scripts/sim.sh [seconds-per-step]
set -euo pipefail

url=${CLAUDE_DISPLAY_URL:-http://127.0.0.1:47800/event}
step=${1:-4}
sid="sim-$$"

post() { # post EVENT [extra JSON fields]
	local extra=${2:+,$2}
	curl -sf -o /dev/null -H 'Content-Type: application/json' "$url" \
		-d "{\"hook_event_name\":\"$1\",\"session_id\":\"$sid\",\"cwd\":\"/tmp/sim-project\"$extra}"
	echo "$1 ${2:-}"
}
trap 'post SessionEnd' EXIT

post SessionStart '"source":"startup"'; sleep "$step"
post UserPromptSubmit; sleep "$step"
post PreToolUse '"tool_name":"Bash"'; sleep "$step"
post PermissionRequest '"tool_name":"Bash"'; sleep $((step * 2))
post PostToolUse '"tool_name":"Bash"'; sleep "$step"
post PreToolUse '"tool_name":"mcp__context7__query-docs"'; sleep "$step"
post PreToolUse '"tool_name":"AskUserQuestion"'; sleep "$step"
post PostToolUse '"tool_name":"AskUserQuestion"'
post PreCompact; sleep "$step"
post PostCompact
post Stop; sleep "$step"
post UserPromptSubmit
post StopFailure '"error":"rate_limit"'; sleep "$step"
