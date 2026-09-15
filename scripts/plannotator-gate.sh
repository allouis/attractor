#!/usr/bin/env bash
# plannotator-gate — bridge a run's plan_gate to Plannotator.
#
# Polls a run's /questions endpoint; when the run parks at the
# plan-review gate, fetches the current plan over the run's HTTP API,
# opens it in Plannotator (review from your laptop over the tailnet),
# and posts the decision back as the gate answer:
#
#   approved  -> [A] Approve plan
#   annotated -> [R] Revise plan, with the feedback as human.note
#   dismissed -> question left pending (the normal UI gate still works)
#
# BASE_URL may be the run's own server OR a hub — the hub proxies
# /questions, /artifacts and /answer through to the run (for VM runs it
# attaches the guest API's bearer token). Everything is read over HTTP;
# no shared filesystem with the run is assumed, so this works for
# local, remote, and VM runs alike.
#
# Usage:
#   scripts/plannotator-gate.sh BASE_URL [RUN_ID] [GATE_NODE] [PLAN_NODE]
# e.g.
#   scripts/plannotator-gate.sh http://127.0.0.1:8080               # single-run server
#   scripts/plannotator-gate.sh http://127.0.0.1:7799 f8f4d39192ca  # via the hub
#
# Requires: curl, jq, plannotator on PATH (or $PLANNOTATOR).
set -euo pipefail

BASE=${1:?usage: plannotator-gate.sh BASE_URL [RUN_ID] [GATE_NODE] [PLAN_NODE]}
RUN_ID=${2:-}
GATE_NODE=${3:-plan_gate}
PLAN_NODE=${4:-plan}
PLANNOTATOR=${PLANNOTATOR:-plannotator}
PORT=${PLANNOTATOR_PORT:-19432}

# Without an explicit RUN_ID, only an unambiguous (single-run) listing
# is accepted — on a hub, "the first run" is whatever sorted first.
if [ -z "$RUN_ID" ]; then
  listing=$(curl -sf "$BASE/pipelines")
  count=$(jq 'length' <<<"$listing")
  if [ "$count" != "1" ]; then
    echo "error: $BASE lists $count runs — pass an explicit RUN_ID" >&2
    exit 2
  fi
  RUN_ID=$(jq -r '.[0].run_id' <<<"$listing")
fi
API="$BASE/pipelines/$RUN_ID"
echo "watching run $RUN_ID for questions on $GATE_NODE (plannotator on :$PORT)"

# latest_plan_span: the plan node's newest span dir by (visit, attempt),
# from the artifact listing (A4: span identity, derived forward).
latest_plan_span() {
  curl -sf "$API/artifacts" |
    jq -r --arg n "$PLAN_NODE" \
      '.[] | capture("^(?<d>" + $n + "@v(?<v>\\d+)\\.a(?<a>\\d+))/status\\.json$") | "\(.v) \(.a) \(.d)"' |
    sort -k1,1n -k2,2n | tail -1 | awk '{print $3}'
}

while :; do
  doc=$(curl -sf "$API") || { echo "run gone"; exit 0; }
  status=$(jq -r '.status' <<<"$doc")
  case "$status" in completed|failed) echo "run $status"; exit 0;; esac

  # A transient proxy failure (guest rebooting, hub scraping) must not
  # kill the watch — only a terminal run status ends it (checked above).
  q=$(curl -sf "$API/questions" 2>/dev/null |
        jq -c --arg n "$GATE_NODE" '[.[] | select(.node_id == $n)][0]' 2>/dev/null || true)
  if [ "$q" = "null" ] || [ -z "$q" ]; then sleep 5; continue; fi
  qid=$(jq -r '.id' <<<"$q")

  # The checkpoint context always holds the CURRENT plan_markdown — a
  # revised plan lands there from revise_plan's span, which a scan of
  # $PLAN_NODE's spans alone would miss (a stale round-1 plan once got
  # re-served at a round-2 gate that way).
  plan_md=$(mktemp --suffix=.md)
  curl -sf "$API/artifacts/checkpoint.json" 2>/dev/null |
    jq -r '.context.plan_markdown // empty' >"$plan_md" || true
  if [ ! -s "$plan_md" ]; then
    # Fall back to the newest plan-node span when the context has no key.
    span=$(latest_plan_span || true)
    if [ -z "$span" ]; then echo "no plan in checkpoint and no $PLAN_NODE span yet?"; sleep 5; continue; fi
    curl -sf "$API/artifacts/$span/status.json" |
      jq -r '.context_updates.plan_markdown // empty' >"$plan_md"
    [ -s "$plan_md" ] || curl -sf "$API/artifacts/$span/response.md" >"$plan_md" || true
  fi
  if [ ! -s "$plan_md" ]; then echo "plan artifact empty?"; sleep 10; continue; fi

  echo "question $qid pending — review at http://$(hostname):$PORT"
  # plannotator refuses a --result-file that already exists; -u names
  # the path without creating it.
  result=$(mktemp -u --suffix=.json)
  PLANNOTATOR_REMOTE=1 PLANNOTATOR_PORT=$PORT BROWSER=none \
    "$PLANNOTATOR" annotate "$plan_md" --gate --json --result-file "$result" || true

  decision=$(jq -r '.decision // "dismissed"' "$result" 2>/dev/null || echo dismissed)
  feedback=$(jq -r '.feedback // ""' "$result" 2>/dev/null || echo "")
  case "$decision" in
    approved)
      echo "approved -> [A]"
      curl -sf -X POST "$API/questions/$qid/answer" \
        -H 'content-type: application/json' \
        -d "$(jq -n --arg n "$feedback" '{value:"A", note:$n}')" >/dev/null ;;
    annotated)
      echo "annotated -> [R] with feedback"
      curl -sf -X POST "$API/questions/$qid/answer" \
        -H 'content-type: application/json' \
        -d "$(jq -n --arg n "$feedback" '{value:"R", note:$n}')" >/dev/null ;;
    *)
      echo "dismissed — question left pending (answer in the run UI, or wait: re-opening in 30s)"
      sleep 30 ;;
  esac
  sleep 2
done
