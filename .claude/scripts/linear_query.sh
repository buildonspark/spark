#!/usr/bin/env bash
set -euo pipefail

# scripts/linear_query.sh
#
# Query Linear issues by:
# - team key (e.g. SPARK)  ✅ supports /team/SPARK/triage style views
# - project name
# - label name
# - workflow state type (e.g. triage)
#
# Usage:
#   export LINEAR_API_KEY="lin_api_..."
#
#   # Team triage view (matches https://linear.app/<org>/team/SPARK/triage)
#   ./scripts/linear_query.sh --team-key "SPARK" --state-type "triage" --limit 50
#
#   # Team triage + only those in a specific project
#   ./scripts/linear_query.sh --team-key "SPARK" --state-type "triage" --project "Spark" --limit 50
#
#   # By project
#   ./scripts/linear_query.sh --project "Spark"
#
#   # By label
#   ./scripts/linear_query.sh --label "Bug"
#
# Output:
#   JSON to stdout (pretty-printed if jq is installed)

ENDPOINT="https://api.linear.app/graphql"

TEAM_KEY=""
LABEL=""
PROJECT=""
STATE_TYPE=""   # triage, backlog, unstarted, started, completed, canceled
LIMIT="20"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --team-key|--team|-t)
      TEAM_KEY="${2:-}"
      shift 2
      ;;
    --label|-l)
      LABEL="${2:-}"
      shift 2
      ;;
    --project|-p)
      PROJECT="${2:-}"
      shift 2
      ;;
    --state-type|--state|-s)
      STATE_TYPE="${2:-}"
      shift 2
      ;;
    --limit)
      LIMIT="${2:-20}"
      shift 2
      ;;
    -h|--help)
      sed -n '1,160p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "Unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

: "${LINEAR_API_KEY:?Set LINEAR_API_KEY (lin_api_...) in your environment}"

# Escape helper for JSON/GraphQL string literals
escape() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  printf '%s' "$s"
}

# Build Linear IssueFilter as JSON text.
# Example fields:
#   team:   { key:  { eq: "SPARK" } }
#   labels: { name: { eq: "Bug" } }
#   project:{ name: { eq: "Spark" } }
#   state:  { type: { eq: "triage" } }
FIELDS=()

if [[ -n "$TEAM_KEY" ]]; then
  esc="$(escape "$TEAM_KEY")"
  FIELDS+=("\"team\": { \"key\": { \"eq\": \"${esc}\" } }")
fi

if [[ -n "$LABEL" ]]; then
  esc="$(escape "$LABEL")"
  FIELDS+=("\"labels\": { \"name\": { \"eq\": \"${esc}\" } }")
fi

if [[ -n "$PROJECT" ]]; then
  esc="$(escape "$PROJECT")"
  FIELDS+=("\"project\": { \"name\": { \"eq\": \"${esc}\" } }")
fi

if [[ -n "$STATE_TYPE" ]]; then
  esc="$(escape "$STATE_TYPE")"
  FIELDS+=("\"state\": { \"type\": { \"eq\": \"${esc}\" } }")
fi

FILTER_JSON='{}'
if [[ ${#FIELDS[@]} -gt 0 ]]; then
  FILTER_JSON="{ $(IFS=','; echo "${FIELDS[*]}") }"
fi

read -r -d '' QUERY <<'GQL' || true
query Issues($limit: Int!, $filter: IssueFilter) {
  issues(first: $limit, filter: $filter, orderBy: updatedAt) {
    nodes {
      id
      identifier
      title
      description
      url
      updatedAt
      state { name type }
      team { key name }
      assignee { name email }
      project { name }
      labels { nodes { name } }
      comments(first: 50) {
        nodes {
          id
          body
          createdAt
          user {
            name
            email
          }
        }
      }
    }
  }
}
GQL

# Build request payload (jq preferred; python fallback)
if command -v jq >/dev/null 2>&1; then
  PAYLOAD="$(
    jq -nc \
      --arg q "$QUERY" \
      --argjson limit "$LIMIT" \
      --arg filter "$FILTER_JSON" \
      '{query: $q, variables: {limit: $limit, filter: ($filter | fromjson)}}'
  )"
else
  PAYLOAD="$(
    python3 - <<PY
import json
query = """$QUERY"""
limit = int("$LIMIT")
filter_obj = json.loads("""$FILTER_JSON""")
print(json.dumps({"query": query, "variables": {"limit": limit, "filter": filter_obj}}))
PY
  )"
fi

RESP="$(
  curl -sS "$ENDPOINT" \
    -H "Authorization: $LINEAR_API_KEY" \
    -H "Content-Type: application/json" \
    --data "$PAYLOAD"
)"

# Print nicely and fail on errors
if command -v jq >/dev/null 2>&1; then
  if [[ "$(echo "$RESP" | jq -r 'has("errors")')" == "true" ]]; then
    echo "$RESP" | jq
    exit 2
  fi
  echo "$RESP" | jq
else
  echo "$RESP"
fi
