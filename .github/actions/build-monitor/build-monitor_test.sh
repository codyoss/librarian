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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET_SCRIPT="$SCRIPT_DIR/build-monitor.sh"
MOCK_DIR="$(mktemp -d)"
trap 'rm -rf "$MOCK_DIR"' EXIT

# Mock gh CLI
cat << 'EOF' > "$MOCK_DIR/gh"
#!/usr/bin/env bash
set -eu
cmd="$1"
subcmd="${2:-}"

case "$cmd" in
  api)
    endpoint="$2"
    case "$endpoint" in
      *check-suites/*/check-runs*)
        if [[ "${MOCK_NO_CHECK_RUNS:-}" == "true" ]]; then
          echo '{"check_runs":[]}'
        else
          echo '{"check_runs":[{"name":"build-target","conclusion":"failure","details_url":"https://ci.example.com/build"}]}'
        fi
        ;;
      *actions/runs/*/jobs*)
        echo '{"jobs":[{"name":"test-job","conclusion":"failure","html_url":"https://github.com/runs/1/job/2"}]}'
        ;;
      *commits/*/pulls*)
        echo '[{"number":42}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    ;;
  issue)
    case "$subcmd" in
      list)
        if [[ -n "${MOCK_EXISTING_ISSUE:-}" ]]; then
          echo "[{\"number\":$MOCK_EXISTING_ISSUE,\"title\":\"librarian: Post-merge build failure on main\"}]"
        else
          echo "[]"
        fi
        ;;
      create)
        if [[ "${MOCK_LABEL_FAIL:-}" == "true" ]] && [[ "$*" == *"--label"* ]]; then
          exit 1
        fi
        echo "https://github.com/googleapis/librarian/issues/100"
        ;;
      comment)
        echo "commented on $3"
        ;;
    esac
    ;;
esac
EOF
chmod +x "$MOCK_DIR/gh"

export PATH="$MOCK_DIR:$PATH"
export GH_REPO="googleapis/librarian"
export TARGET_BRANCH="main"
export CHECK_SUITE_APP_NAME="Google Cloud Build"

run_test() {
  local name="$1"
  shift
  local output
  output=$(env "$@" bash "$TARGET_SCRIPT" 2>&1) || {
    echo "FAIL: $name (exited with error)" >&2
    echo "$output" >&2
    return 1
  }
  echo "$output"
}

echo "Running build-monitor tests..."

# 1. check_suite valid failure creates issue
out=$(run_test "check_suite failure" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="failure" \
  CHECK_SUITE_HEAD_BRANCH="main" \
  CHECK_SUITE_APP_NAME_ACTUAL="Google Cloud Build" \
  CHECK_SUITE_ID="123" \
  CHECK_SUITE_HEAD_SHA="abcdef123456" \
  TEAM_MENTION="@googleapis/test-team")
[[ "$out" == *"Created issue:"* ]] || { echo "Expected issue created, got: $out"; exit 1; }
[[ "$out" == *"commented on 100"* ]] || { echo "Expected team mention comment, got: $out"; exit 1; }

# 2. check_suite with existing issue adds comment instead
out=$(run_test "check_suite existing issue" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="failure" \
  CHECK_SUITE_HEAD_BRANCH="main" \
  CHECK_SUITE_APP_NAME_ACTUAL="Google Cloud Build" \
  CHECK_SUITE_ID="123" \
  CHECK_SUITE_HEAD_SHA="abcdef123456" \
  MOCK_EXISTING_ISSUE="42")
[[ "$out" == *"Found existing open issue #42. Adding comment."* ]] || { echo "Expected comment, got: $out"; exit 1; }

# 3. check_suite success skips
out=$(run_test "check_suite success" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="success")
[[ "$out" == *"is not a failure. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 4. check_suite wrong branch skips
out=$(run_test "check_suite wrong branch" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="failure" \
  CHECK_SUITE_HEAD_BRANCH="feat/test")
[[ "$out" == *"does not match 'main'. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 5. check_suite wrong app skips
out=$(run_test "check_suite wrong app" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="failure" \
  CHECK_SUITE_HEAD_BRANCH="main" \
  CHECK_SUITE_APP_NAME_ACTUAL="Random App")
[[ "$out" == *"does not match 'Google Cloud Build'. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 6. check_suite security: open PR skips
out=$(run_test "check_suite open PR security" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="failure" \
  CHECK_SUITE_HEAD_BRANCH="main" \
  CHECK_SUITE_APP_NAME_ACTUAL="Google Cloud Build" \
  CHECK_SUITE_PULL_REQUESTS='[{"number":1,"state":"open","head":{"repo":{"full_name":"googleapis/librarian"}}}]')
[[ "$out" == *"associated with an active or fork pull request. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 7. check_suite security: fork PR with null head.repo skips
out=$(run_test "check_suite null head repo security" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="failure" \
  CHECK_SUITE_HEAD_BRANCH="main" \
  CHECK_SUITE_APP_NAME_ACTUAL="Google Cloud Build" \
  CHECK_SUITE_PULL_REQUESTS='[{"number":1,"state":"closed","head":{"repo":null}}]')
[[ "$out" == *"associated with an active or fork pull request. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 8. workflow_run valid failure creates issue
out=$(run_test "workflow_run failure" \
  EVENT_NAME="workflow_run" \
  EVENT_ACTION="completed" \
  WORKFLOW_RUN_CONCLUSION="failure" \
  WORKFLOW_RUN_EVENT="push" \
  WORKFLOW_RUN_HEAD_BRANCH="main" \
  WORKFLOW_RUN_HEAD_REPO="googleapis/librarian" \
  WORKFLOW_RUN_ID="789" \
  WORKFLOW_RUN_HEAD_SHA="abcdef123456")
[[ "$out" == *"Created issue:"* ]] || { echo "Expected issue created, got: $out"; exit 1; }

# 9. workflow_run non-push event skips
out=$(run_test "workflow_run pull_request event" \
  EVENT_NAME="workflow_run" \
  EVENT_ACTION="completed" \
  WORKFLOW_RUN_CONCLUSION="failure" \
  WORKFLOW_RUN_EVENT="pull_request")
[[ "$out" == *"is not push. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 10. workflow_run fork repo skips
out=$(run_test "workflow_run fork repo" \
  EVENT_NAME="workflow_run" \
  EVENT_ACTION="completed" \
  WORKFLOW_RUN_CONCLUSION="failure" \
  WORKFLOW_RUN_EVENT="push" \
  WORKFLOW_RUN_HEAD_REPO="malicious-fork/librarian")
[[ "$out" == *"does not match 'googleapis/librarian'. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 11. extra event (push, workflow_dispatch, pull_request) skips immediately
for extra in push workflow_dispatch pull_request release; do
  out=$(run_test "extra event $extra" EVENT_NAME="$extra")
  [[ "$out" == *"Event '$extra' is not monitored. Skipping."* ]] || { echo "Expected skip for $extra, got: $out"; exit 1; }
done

# 12. workflow_run security: open PR skips
out=$(run_test "workflow_run open PR security" \
  EVENT_NAME="workflow_run" \
  EVENT_ACTION="completed" \
  WORKFLOW_RUN_CONCLUSION="failure" \
  WORKFLOW_RUN_EVENT="push" \
  WORKFLOW_RUN_HEAD_BRANCH="main" \
  WORKFLOW_RUN_HEAD_REPO="googleapis/librarian" \
  WORKFLOW_RUN_PULL_REQUESTS='[{"number":1,"state":"open","head":{"repo":{"full_name":"googleapis/librarian"}}}]')
[[ "$out" == *"associated with an active or fork pull request. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 13. workflow_run security: fork PR with null head.repo skips
out=$(run_test "workflow_run null head repo security" \
  EVENT_NAME="workflow_run" \
  EVENT_ACTION="completed" \
  WORKFLOW_RUN_CONCLUSION="failure" \
  WORKFLOW_RUN_EVENT="push" \
  WORKFLOW_RUN_HEAD_BRANCH="main" \
  WORKFLOW_RUN_HEAD_REPO="googleapis/librarian" \
  WORKFLOW_RUN_PULL_REQUESTS='[{"number":1,"state":"closed","head":{"repo":null}}]')
[[ "$out" == *"associated with an active or fork pull request. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 14. non-completed action skips
out=$(run_test "non-completed action" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="requested")
[[ "$out" == *"is not completed. Skipping."* ]] || { echo "Expected skip, got: $out"; exit 1; }

# 15. check_suite with no failed check runs falls back to commit checks link
out=$(run_test "check_suite empty runs fallback" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="failure" \
  CHECK_SUITE_HEAD_BRANCH="main" \
  CHECK_SUITE_APP_NAME_ACTUAL="Google Cloud Build" \
  CHECK_SUITE_ID="123" \
  CHECK_SUITE_HEAD_SHA="abcdef123456" \
  MOCK_NO_CHECK_RUNS="true")
[[ "$out" == *"Created issue:"* ]] || { echo "Expected issue created, got: $out"; exit 1; }

# 16. issue creation with label failure retries without label
out=$(run_test "issue creation label failure fallback" \
  EVENT_NAME="check_suite" \
  EVENT_ACTION="completed" \
  CHECK_SUITE_CONCLUSION="failure" \
  CHECK_SUITE_HEAD_BRANCH="main" \
  CHECK_SUITE_APP_NAME_ACTUAL="Google Cloud Build" \
  CHECK_SUITE_ID="123" \
  CHECK_SUITE_HEAD_SHA="abcdef123456" \
  MOCK_LABEL_FAIL="true")
[[ "$out" == *"Created issue:"* ]] || { echo "Expected issue created with fallback, got: $out"; exit 1; }

echo "All build-monitor tests passed successfully!"
