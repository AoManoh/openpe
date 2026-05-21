---
description: Enhance a raw task prompt with openPE, then execute the enhanced prompt in the current Codex session.
argument-hint: [raw task prompt]
---

# openPE Prompt Enhancement

Use openPE to enhance the user's raw task prompt, then execute the enhanced prompt in this same Codex session.

## Arguments

The user invoked this command with:

```text
$ARGUMENTS
```

If `$ARGUMENTS` is empty or only whitespace, ask the user for the task prompt and stop.

## Workflow

1. Capture the current workspace path before changing directories:

```bash
TARGET_CWD="$(pwd)"
```

2. Run openPE with the raw arguments as stdin. Load the local openPE `.env` if it exists, but never print secret values:

```bash
OPENPE_PROJECT="/home/oh/projects/openPE"
TARGET_CWD="$(pwd)"

set -a
if [ -f "$OPENPE_PROJECT/.env" ]; then
  # shellcheck disable=SC1091
  . "$OPENPE_PROJECT/.env"
fi
set +a

if command -v openpe >/dev/null 2>&1; then
  cat <<'OPENPE_RAW_PROMPT' | openpe enhance --client codex --mode agent --cwd "$TARGET_CWD"
$ARGUMENTS
OPENPE_RAW_PROMPT
else
  cat <<'OPENPE_RAW_PROMPT' | (cd "$OPENPE_PROJECT" && go run ./cmd/openpe enhance --client codex --mode agent --cwd "$TARGET_CWD")
$ARGUMENTS
OPENPE_RAW_PROMPT
fi
```

3. Treat stdout from the command as the enhanced prompt. If the command fails or prints an empty prompt, report the error and stop.

4. Execute the enhanced prompt as the user's current task in this same Codex session. Do not start `codex exec` or a nested Codex process; this slash command is already running inside Codex.

## Guardrails

- Do not fall back to the unenhanced prompt unless the user explicitly asks.
- Do not print API keys, tokens, or values loaded from `.env`.
- Preserve the current workspace as the task target; `/home/oh/projects/openPE` is only the enhancer implementation path.
