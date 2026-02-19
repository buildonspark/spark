#!/bin/bash
# PreToolUse hook: reminds Claude to follow the PR template when submitting PRs

INPUT=$(cat)
COMMAND=$(echo "$INPUT" | jq -r '.tool_input.command // empty')

if [[ "$COMMAND" =~ ^gt\ submit ]] || [[ "$COMMAND" =~ ^gh\ pr\ create ]]; then
  TEMPLATE=""
  if [ -f .github/PULL_REQUEST_TEMPLATE.md ]; then
    TEMPLATE=$(cat .github/PULL_REQUEST_TEMPLATE.md)
  fi

  if [ -n "$TEMPLATE" ]; then
    jq -n --arg template "$TEMPLATE" '{
      "hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "additionalContext": ("IMPORTANT: Follow the PR template format from .github/PULL_REQUEST_TEMPLATE.md:\n\n" + $template)
      }
    }'
    exit 0
  fi
fi

exit 0
