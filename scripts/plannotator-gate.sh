#!/usr/bin/env bash
# plannotator-gate — bridge a run's plan_gate to Plannotator.
#
# Polls a live run's /questions endpoint; when the run parks at the
# plan-review gate, extracts the plan from the plan span's status.json,
# opens it in Plannotator (review from your laptop over the tailnet),
# and posts the decision back as the gate answer:
#
#   approved  -> [A] Approve plan
#   annotated -> [R] Revise plan, with the feedback as human.note
#   dismissed -> question left pending (the normal UI gate still works)
#
# Usage:
#   scripts/plannotator-gate.sh BASE_URL LOGS_DIR [GATE_NODE] [PLAN_NODE]
# e.g.
#   scripts/plannotator-gate.sh http://127.0.0.1:8080 ~/.attractor/runs/<id>
#
# Requires: curl, jq, plannotator on PATH (or $PLANNOTATOR).
set -euo pipefail

BASE=${1:?usage: plannotator-gate.sh BASE_URL LOGS_DIR [GATE_NODE] [PLAN_NODE]}
LOGS=${2:?need LOGS_DIR (the run directory, for plan_markdown)}
GATE_NODE=${3:-plan_gate}
PLAN_NODE=${4:-plan}
PLANNOTATOR=${PLANNOTATOR:-plannotator}
PORT=${PLANNOTATOR_PORT:-19432}

run_id=$(curl -sf "$BASE/pipelines" | jq -r '.[0].run_id')
echo "watching run $run_id for questions on $GATE_NODE (plannotator on :$PORT)"

# latest_plan_status: the plan node's newest span dir by (visit, attempt).
latest_plan_status() {
  ls -d "$LOGS/$PLAN_NODE"@v*.a*/ 2>/dev/null |
    sed -E 's/.*@v([0-9]+)\.a([0-9]+)\/$/\1 \2 &/' |
    sort -k1,1n -k2,2n | tail -1 | awk '{print $3}'
}

while :; do
  doc=$(curl -sf "$BASE/pipelines/$run_id" ) || { echo "run gone"; exit 0; }
  status=$(jq -r '.status' <<<"$doc")
  case "$status" in completed|failed) echo "run $status"; exit 0;; esac

  q=$(curl -sf "$BASE/pipelines/$run_id/questions" |
        jq -c --arg n "$GATE_NODE" '[.[] | select(.node_id == $n)][0]')
  if [ "$q" = "null" ] || [ -z "$q" ]; then sleep 5; continue; fi
  qid=$(jq -r '.id' <<<"$q")

  span=$(latest_plan_status)
  if [ -z "$span" ]; then echo "no $PLAN_NODE span yet?"; sleep 5; continue; fi
  plan_md=$(mktemp --suffix=.md)
  jq -r '.context_updates.plan_markdown // empty' "$span/status.json" >"$plan_md"
  if [ ! -s "$plan_md" ]; then
    # Fall back to the raw response when the agent didn't set the key.
    cp "$span/response.md" "$plan_md"
  fi

  echo "question $qid pending — review at http://$(hostname):$PORT"
  result=$(mktemp --suffix=.json)
  PLANNOTATOR_REMOTE=1 PLANNOTATOR_PORT=$PORT BROWSER=none \
    "$PLANNOTATOR" annotate "$plan_md" --gate --json --result-file "$result" || true

  decision=$(jq -r '.decision // "dismissed"' "$result" 2>/dev/null || echo dismissed)
  feedback=$(jq -r '.feedback // ""' "$result" 2>/dev/null || echo "")
  case "$decision" in
    approved)
      echo "approved -> [A]"
      curl -sf -X POST "$BASE/pipelines/$run_id/questions/$qid/answer" \
        -H 'content-type: application/json' \
        -d "$(jq -n --arg n "$feedback" '{value:"A", note:$n}')" >/dev/null ;;
    annotated)
      echo "annotated -> [R] with feedback"
      curl -sf -X POST "$BASE/pipelines/$run_id/questions/$qid/answer" \
        -H 'content-type: application/json' \
        -d "$(jq -n --arg n "$feedback" '{value:"R", note:$n}')" >/dev/null ;;
    *)
      echo "dismissed — question left pending (answer in the run UI, or wait: re-opening in 30s)"
      sleep 30 ;;
  esac
  sleep 2
done
