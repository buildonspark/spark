#!/bin/bash
set -e

INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd')
PLANS_DIR="${CWD}/.claude/plans"

# Find most recently modified plan file
LATEST_PLAN=$(ls -t "${PLANS_DIR}"/*.md 2>/dev/null | head -1)
[ -z "$LATEST_PLAN" ] && exit 0

ORIGINAL_NAME=$(basename "$LATEST_PLAN" .md)

# Skip if already renamed (contains underscore-separated date pattern)
if [[ "$ORIGINAL_NAME" =~ ^.*_[0-9]{8}-[0-9]{6}_ ]]; then
  exit 0
fi

# Get branch name, sanitize
BRANCH=$(git -C "$CWD" rev-parse --abbrev-ref HEAD 2>/dev/null | tr '/' '-')
[ -z "$BRANCH" ] && exit 0

# Get datetime from file modification time (reflects when plan was created, not renamed)
if [[ "$(uname)" == "Darwin" ]]; then
  TIMESTAMP=$(stat -f '%Sm' -t '%Y%m%d-%H%M%S' "$LATEST_PLAN")
else
  TIMESTAMP=$(date -d "@$(stat -c '%Y' "$LATEST_PLAN")" '+%Y%m%d-%H%M%S')
fi

# Extract first heading, slugify
HEADING=$(grep -m1 '^#' "$LATEST_PLAN" | sed -E 's/^#+ *//' | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/-/g' | sed -E 's/-+/-/g' | sed 's/^-//;s/-$//')
[ -z "$HEADING" ] && HEADING="$ORIGINAL_NAME"

NEW_NAME="${BRANCH}_${TIMESTAMP}_${HEADING}.md"
mv "$LATEST_PLAN" "${PLANS_DIR}/${NEW_NAME}"

# Tell Claude about the rename
cat <<EOF
{"additionalContext": "Plan file renamed to: .claude/plans/${NEW_NAME}"}
EOF

exit 0
