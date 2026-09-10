#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

GH_REPO="${GH_REPO:-${GITHUB_REPOSITORY:-}}"
TARGET_BRANCH="${TARGET_BRANCH:-main}"
CHECK_SUITE_APP_NAME="${CHECK_SUITE_APP_NAME:-Google Cloud Build}"
ISSUE_LABELS="${ISSUE_LABELS-:rotating_light: critical}"
EVENT_NAME="${EVENT_NAME:-}"
EVENT_ACTION="${EVENT_ACTION:-}"

if [[ -z "$GH_REPO" ]]; then
  echo "Error: GH_REPO is required." >&2
  exit 1
fi

if [[ -n "$EVENT_ACTION" && "$EVENT_ACTION" != "completed" ]]; then
  echo "Event action '$EVENT_ACTION' is not completed. Skipping."
  exit 0
fi

COMMIT_SHA=""
BODY=""

case "$EVENT_NAME" in
  check_suite)
    case "${CHECK_SUITE_CONCLUSION:-}" in
      failure|timed_out|cancelled) ;;
      *)
        echo "Check suite conclusion '${CHECK_SUITE_CONCLUSION:-}' is not a failure. Skipping."
        exit 0
        ;;
    esac

    if [[ "${CHECK_SUITE_HEAD_BRANCH:-}" != "$TARGET_BRANCH" ]]; then
      echo "Check suite branch '${CHECK_SUITE_HEAD_BRANCH:-}' does not match '$TARGET_BRANCH'. Skipping."
      exit 0
    fi

    if [[ "${CHECK_SUITE_APP_NAME_ACTUAL:-}" != "$CHECK_SUITE_APP_NAME" ]]; then
      echo "Check suite app '${CHECK_SUITE_APP_NAME_ACTUAL:-}' does not match '$CHECK_SUITE_APP_NAME'. Skipping."
      exit 0
    fi

    # Security check: Skip if check suite is associated with open PRs or PRs from forks.
    # Optional chaining (.head?.repo?.full_name) handles cases where .head.repo is null.
    if [[ -n "${CHECK_SUITE_PULL_REQUESTS:-}" && "$CHECK_SUITE_PULL_REQUESTS" != "[]" ]]; then
      IS_UNTRUSTED=$(echo "$CHECK_SUITE_PULL_REQUESTS" | jq -r --arg repo "$GH_REPO" '
        [ .[]? | select(.state == "open" or (.head?.repo?.full_name // "") != $repo) ] | length > 0
      ' 2>/dev/null || echo "true")
      if [[ "$IS_UNTRUSTED" == "true" ]]; then
        echo "Check suite is associated with an active or fork pull request. Skipping."
        exit 0
      fi
    fi

    COMMIT_SHA="${CHECK_SUITE_HEAD_SHA:-}"

    FAILED_BUILDS=""
    if [[ -n "${CHECK_SUITE_ID:-}" ]]; then
      FAILED_BUILDS=$(gh api "repos/$GH_REPO/check-suites/$CHECK_SUITE_ID/check-runs" --paginate 2>/dev/null | jq -r '
        .check_runs[]?
        | select(.conclusion == "failure" or .conclusion == "timed_out" or .conclusion == "cancelled")
        | if (.details_url // .html_url // "") != "" then "- [`" + .name + "`](" + (.details_url // .html_url) + ")" else "- `" + .name + "`" end
      ' 2>/dev/null || true)
    fi

    if [[ -z "$FAILED_BUILDS" ]]; then
      if [[ -n "$COMMIT_SHA" ]]; then
        FAILED_BUILDS="- [Check Suite Logs](https://github.com/$GH_REPO/commit/$COMMIT_SHA/checks)"
      else
        FAILED_BUILDS="- Check suite failed"
      fi
    fi

    BODY="**Failure in $CHECK_SUITE_APP_NAME on \`$TARGET_BRANCH\`:**

**Failed Builds:**
$FAILED_BUILDS

**Commit:** $COMMIT_SHA"
    ;;

  workflow_run)
    case "${WORKFLOW_RUN_CONCLUSION:-}" in
      failure|timed_out|cancelled|startup_failure) ;;
      *)
        echo "Workflow run conclusion '${WORKFLOW_RUN_CONCLUSION:-}' is not a failure. Skipping."
        exit 0
        ;;
    esac

    # Only monitor workflow runs triggered by push to main (ignores pull_request runs)
    if [[ "${WORKFLOW_RUN_EVENT:-}" != "push" ]]; then
      echo "Workflow run triggering event '${WORKFLOW_RUN_EVENT:-}' is not push. Skipping."
      exit 0
    fi

    if [[ -n "${WORKFLOW_RUN_HEAD_REPO:-}" && "$WORKFLOW_RUN_HEAD_REPO" != "$GH_REPO" ]]; then
      echo "Workflow run repo '$WORKFLOW_RUN_HEAD_REPO' does not match '$GH_REPO'. Skipping."
      exit 0
    fi

    if [[ "${WORKFLOW_RUN_HEAD_BRANCH:-}" != "$TARGET_BRANCH" ]]; then
      echo "Workflow run branch '${WORKFLOW_RUN_HEAD_BRANCH:-}' does not match '$TARGET_BRANCH'. Skipping."
      exit 0
    fi

    # Security check: Skip if workflow run is associated with open PRs or PRs from forks.
    if [[ -n "${WORKFLOW_RUN_PULL_REQUESTS:-}" && "$WORKFLOW_RUN_PULL_REQUESTS" != "[]" ]]; then
      IS_UNTRUSTED=$(echo "$WORKFLOW_RUN_PULL_REQUESTS" | jq -r --arg repo "$GH_REPO" '
        [ .[]? | select(.state == "open" or (.head?.repo?.full_name // "") != $repo) ] | length > 0
      ' 2>/dev/null || echo "true")
      if [[ "$IS_UNTRUSTED" == "true" ]]; then
        echo "Workflow run is associated with an active or fork pull request. Skipping."
        exit 0
      fi
    fi

    COMMIT_SHA="${WORKFLOW_RUN_HEAD_SHA:-}"

    FAILED_JOBS=""
    if [[ -n "${WORKFLOW_RUN_ID:-}" ]]; then
      FAILED_JOBS=$(gh api "repos/$GH_REPO/actions/runs/$WORKFLOW_RUN_ID/jobs" --paginate 2>/dev/null | jq -r '
        .jobs[]?
        | select(.conclusion == "failure" or .conclusion == "timed_out" or .conclusion == "cancelled" or .conclusion == "startup_failure")
        | if (.html_url // "") != "" then "- [`" + .name + "`](" + .html_url + ")" else "- `" + .name + "`" end
      ' 2>/dev/null || true)
    fi

    if [[ -z "$FAILED_JOBS" ]]; then
      if [[ -n "${WORKFLOW_RUN_URL:-}" ]]; then
        FAILED_JOBS="- [Workflow Run Logs]($WORKFLOW_RUN_URL)"
      else
        FAILED_JOBS="- Workflow run failed"
      fi
    fi

    WORKFLOW_NAME="${WORKFLOW_RUN_NAME:-GitHub Actions}"
    WORKFLOW_TEXT="**Workflow:** $WORKFLOW_NAME"
    if [[ -n "${WORKFLOW_RUN_URL:-}" ]]; then
      WORKFLOW_TEXT="$WORKFLOW_TEXT ([Run Logs]($WORKFLOW_RUN_URL))"
    fi

    BODY="**Failure in GitHub Actions on \`$TARGET_BRANCH\`:**

$WORKFLOW_TEXT

**Failed Jobs:**
$FAILED_JOBS

**Commit:** $COMMIT_SHA"
    ;;

  *)
    echo "Event '$EVENT_NAME' is not monitored. Skipping."
    exit 0
    ;;
esac

# Find associated Pull Requests
PR_TEXT=""
if [[ -n "$COMMIT_SHA" ]]; then
  PR_TEXT=$(gh api "repos/$GH_REPO/commits/$COMMIT_SHA/pulls" 2>/dev/null | jq -r '
    [ .[]?.number | "- #" + tostring ] | unique | join("\n")
  ' 2>/dev/null || true)
fi

if [[ -z "$PR_TEXT" ]]; then
  PAYLOAD_PRS="${CHECK_SUITE_PULL_REQUESTS:-${WORKFLOW_RUN_PULL_REQUESTS:-}}"
  if [[ -n "$PAYLOAD_PRS" && "$PAYLOAD_PRS" != "[]" ]]; then
    PR_TEXT=$(echo "$PAYLOAD_PRS" | jq -r '
      [ .[]?.number | "- #" + tostring ] | unique | join("\n")
    ' 2>/dev/null || true)
  fi
fi

if [[ -n "$PR_TEXT" ]]; then
  BODY="$BODY

**Associated Pull Requests:**
$PR_TEXT"
fi

TITLE="${ISSUE_TITLE_INPUT:-}"
if [[ -z "$TITLE" ]]; then
  REPO_NAME="${GH_REPO##*/}"
  TITLE="$REPO_NAME: Post-merge build failure on $TARGET_BRANCH"
fi

# Check for existing open issue with exact title match
EXISTING_ISSUE=$(gh issue list --repo "$GH_REPO" --search "\"$TITLE\" in:title" --state open --json number,title 2>/dev/null | jq -r --arg title "$TITLE" '
  [ .[]? | select(.title == $title) ][0].number // empty
' 2>/dev/null || true)

if [[ -n "$EXISTING_ISSUE" && "$EXISTING_ISSUE" != "null" ]]; then
  echo "Found existing open issue #$EXISTING_ISSUE. Adding comment."
  gh issue comment "$EXISTING_ISSUE" --repo "$GH_REPO" --body "The build failed again on \`$TARGET_BRANCH\`.

$BODY"
  exit 0
fi

echo "Creating new issue: $TITLE"
CREATE_CMD=(gh issue create --title "$TITLE" --body "$BODY" --repo "$GH_REPO")
if [[ -n "$ISSUE_LABELS" ]]; then
  CREATE_CMD+=(--label "$ISSUE_LABELS")
fi

if ! ISSUE_LINK=$("${CREATE_CMD[@]}" 2>/dev/null); then
  ISSUE_LINK=$(gh issue create --title "$TITLE" --body "$BODY" --repo "$GH_REPO")
fi

ISSUE_LINK=$(echo "$ISSUE_LINK" | tr -d '[:space:]')
echo "Created issue: $ISSUE_LINK"

if [[ -n "${TEAM_MENTION:-}" && -n "$ISSUE_LINK" ]]; then
  ISSUE_NUM="${ISSUE_LINK##*/}"
  if [[ -n "$ISSUE_NUM" ]]; then
    gh issue comment "$ISSUE_NUM" --repo "$GH_REPO" --body "$TEAM_MENTION A critical issue has been created, please respond immediately."
  fi
fi
