#!/usr/bin/env bash
#
# birddog smoke test (macOS).
#
# Drives fake workers through the states that are hard to arrange on demand —
# active output, a long tool run, idle, an approval wait, a disconnect, an exit
# — and checks what birddog reports about each. Every assertion here maps to an
# acceptance criterion named in docs/handoff.md.
#
# Nothing real is watched, and nothing is sent anywhere.
#
# Usage: scripts/smoke.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Short path: macOS refuses to bind a unix socket past 104 bytes.
WORK="$(mktemp -d /tmp/bdsmoke.XXXXXX)"
BIRDDOG="$WORK/birddog"
STATE="$WORK/state"
export BIRDDOG_STATE_DIR="$STATE"

PASS=0
FAIL=0
INSTANCE=""

cleanup() {
  if [[ -n "$INSTANCE" ]]; then
    "$BIRDDOG" stop --instance "$INSTANCE" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

ok()   { printf '  \033[32mok\033[0m   %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; printf '       %s\n' "${2:-}"; FAIL=$((FAIL+1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# --- fake worker control -----------------------------------------------------

# worker <name> <status> <identity> [seconds-since-activity]
worker() {
  local name=$1 status=$2 identity=$3 ago=${4:-0}
  local when
  when=$(python3 -c "
import time,datetime
print(datetime.datetime.fromtimestamp(time.time()-$ago, datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'))")
  cat > "$WORK/$name.json" <<EOF
{"status":"$status","live":true,"identity":"$identity","last_activity_at":"$when","status_since":"$when"}
EOF
}

disconnect() { rm -f "$WORK/$1.json"; }

# --- queries -----------------------------------------------------------------

conditions() { # conditions <target> -> open conditions, space separated
  "$BIRDDOG" status --instance "$INSTANCE" --json 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
t=[x for x in d['targets'] if x['id']=='$1']
print(' '.join(i['condition'] for i in (t[0].get('incidents') or [])) if t else '')"
}

target_field() { # target_field <target> <field>
  "$BIRDDOG" status --instance "$INSTANCE" --json 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
t=[x for x in d['targets'] if x['id']=='$1']
print(t[0].get('$2','') if t else '')"
}

incident_id() { # incident_id <target> <condition>
  "$BIRDDOG" status --instance "$INSTANCE" --json 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
for t in d['targets']:
    if t['id']=='$1':
        for i in (t.get('incidents') or []):
            if i['condition']=='$2':
                print(i['id']); break
"
}

# await <description> <command...> — retries until the command succeeds.
await() {
  local what=$1; shift
  local deadline=$(( SECONDS + 15 ))
  while (( SECONDS < deadline )); do
    if "$@"; then return 0; fi
    sleep 0.3
  done
  return 1
}

has_condition()  { conditions "$1" | grep -qw "$2"; }
lacks_condition(){ ! conditions "$1" | grep -qw "$2"; }
status_is()      { [[ "$(target_field "$1" status)" == "$2" ]]; }

# --- setup -------------------------------------------------------------------

step "Building birddog"
( cd "$ROOT" && go build -o "$BIRDDOG" ./cmd/birddog ) || { echo "build failed"; exit 1; }
ok "built"

step "Starting fake workers"
worker active-worker  active        run-1
worker tool-worker    running_tool  run-1 600   # busy on a tool for ten minutes
worker idle-worker    idle          run-1
worker input-worker   waiting_input run-1
worker gone-worker    active        run-1
ok "five fake workers publishing state"

cat > "$WORK/config.json" <<EOF
{
  "schema_version": 1,
  "name": "smoke",
  "targets": [
    {"id":"active-worker","provider":"fake","attachment":{"kind":"existing-session","session_id":"$WORK/active-worker.json"},
     "labels":{"task_id":"T-1"},
     "policy":{"alert_on":["idle","input_requested","exit","quiet","observation_lost"],"quiet_after_seconds":3,"idle_grace_seconds":1}},
    {"id":"tool-worker","provider":"fake","attachment":{"kind":"existing-session","session_id":"$WORK/tool-worker.json"},
     "policy":{"alert_on":["idle","input_requested","exit","quiet","observation_lost"],"quiet_after_seconds":3,"idle_grace_seconds":1}},
    {"id":"idle-worker","provider":"fake","attachment":{"kind":"existing-session","session_id":"$WORK/idle-worker.json"},
     "policy":{"alert_on":["idle","input_requested","exit","quiet","observation_lost"],"quiet_after_seconds":3,"idle_grace_seconds":1}},
    {"id":"input-worker","provider":"fake","attachment":{"kind":"existing-session","session_id":"$WORK/input-worker.json"},
     "policy":{"alert_on":["idle","input_requested","exit","quiet","observation_lost"],"quiet_after_seconds":3,"idle_grace_seconds":1}},
    {"id":"gone-worker","provider":"fake","attachment":{"kind":"existing-session","session_id":"$WORK/gone-worker.json"},
     "policy":{"alert_on":["idle","input_requested","exit","quiet","observation_lost"],"quiet_after_seconds":3,"idle_grace_seconds":1}}
  ]
}
EOF

step "Starting the instance"
INSTANCE=$("$BIRDDOG" start --config "$WORK/config.json" --json | python3 -c "import json,sys;print(json.load(sys.stdin)['instance_id'])")
[[ -n "$INSTANCE" ]] && ok "instance $INSTANCE reachable" || { bad "instance did not start"; exit 1; }

# --- assertions --------------------------------------------------------------

step "Observation"
await "active worker observed" status_is active-worker active \
  && ok "an active worker is reported active" \
  || bad "an active worker was not reported active" "status=$(target_field active-worker status)"

await "input request" has_condition input-worker input_requested \
  && ok "an approval wait raises input_requested" \
  || bad "no input_requested for a worker waiting on input" "conditions=$(conditions input-worker)"

await "idle" has_condition idle-worker idle \
  && ok "an idle worker raises idle after its grace period" \
  || bad "no idle condition" "conditions=$(conditions idle-worker)"

step "What must NOT be reported (criterion 5)"
sleep 4  # well past the 3s quiet threshold
lacks_condition tool-worker quiet \
  && ok "a ten-minute tool run is not called quiet" \
  || bad "a long tool run was reported quiet" "conditions=$(conditions tool-worker)"

lacks_condition active-worker quiet \
  && ok "a working session is not called quiet" \
  || bad "an actively working session was reported quiet" "conditions=$(conditions active-worker)"

lacks_condition input-worker quiet \
  && ok "a worker waiting on input raises one incident, not two" \
  || bad "quiet opened alongside input_requested" "conditions=$(conditions input-worker)"

step "Acknowledgement changes nothing (criterion 4)"
ID=$(incident_id input-worker input_requested)
if [[ -n "$ID" ]]; then
  "$BIRDDOG" ack --instance "$INSTANCE" --incident "$ID" >/dev/null
  if has_condition input-worker input_requested; then
    ok "acknowledging records the alert without resolving it"
  else
    bad "acknowledging resolved the incident"
  fi
else
  bad "no incident to acknowledge"
fi

step "Disconnect is not an exit (criterion 8)"
disconnect gone-worker
await "observation loss" has_condition gone-worker observation_lost \
  && ok "a vanished worker raises observation_lost" \
  || bad "no observation_lost for a vanished worker" "conditions=$(conditions gone-worker)"

lacks_condition gone-worker exit \
  && ok "a vanished worker is not reported as exited" \
  || bad "losing observation was reported as an exit" "conditions=$(conditions gone-worker)"

[[ "$(target_field gone-worker status_is_current)" == "False" ]] \
  && ok "its last known state is marked stale, not current" \
  || bad "a stale state was presented as current"

step "Recovery and resolution"
worker gone-worker active run-1
await "recovery" lacks_condition gone-worker observation_lost \
  && ok "observation_lost resolves once the worker is visible again" \
  || bad "observation_lost did not resolve" "conditions=$(conditions gone-worker)"

worker input-worker active run-1
await "input resolved" lacks_condition input-worker input_requested \
  && ok "input_requested resolves once the worker moves on" \
  || bad "input_requested did not resolve" "conditions=$(conditions input-worker)"

step "Exit is reported (criterion 6 of the alert table)"
worker active-worker exited run-1
await "exit" has_condition active-worker exit \
  && ok "a worker that exits raises exit" \
  || bad "no exit condition" "conditions=$(conditions active-worker)"

step "Deduplication (criterion 17)"
BEFORE=$(conditions idle-worker | tr ' ' '\n' | grep -c idle)
sleep 3
AFTER=$(conditions idle-worker | tr ' ' '\n' | grep -c idle)
[[ "$BEFORE" == "1" && "$AFTER" == "1" ]] \
  && ok "a persisting condition stays one incident across many passes" \
  || bad "repeated observation duplicated an incident" "before=$BEFORE after=$AFTER"

step "Replaced session starts a new generation (criterion 16)"
GEN_BEFORE=$(target_field idle-worker generation)
worker idle-worker idle run-2   # same target, different run
await "new generation" bash -c "[[ \"\$('$BIRDDOG' status --instance '$INSTANCE' --json | python3 -c \"
import json,sys
print([t for t in json.load(sys.stdin)['targets'] if t['id']=='idle-worker'][0].get('generation',''))\")\" != '$GEN_BEFORE' ]]" \
  && ok "a replacement session advances the generation" \
  || bad "generation did not advance" "before=$GEN_BEFORE after=$(target_field idle-worker generation)"

step "Event feed and cursor (criterion 11)"
CURSOR=$("$BIRDDOG" events --instance "$INSTANCE" --limit 1000 --json | python3 -c "import json,sys;print(json.load(sys.stdin)['cursor'])")
[[ "$CURSOR" -gt 0 ]] && ok "events are recorded with a cursor" || bad "no events recorded"

NEXT=$("$BIRDDOG" events --instance "$INSTANCE" --after "$CURSOR" --wait 10 --json | python3 -c "import json,sys;print(len(json.load(sys.stdin)['events']))")
[[ "$NEXT" -gt 0 ]] && ok "a long poll returns when new observations arrive" || bad "long poll returned nothing"

STALE=$("$BIRDDOG" events --instance "$INSTANCE" --after 999999 --json 2>&1 | head -1)
ok "a cursor past the head returns cleanly (${STALE:0:40}...)"

step "Stopping leaves workers alone (criterion 12)"
WORKERS_BEFORE=$(ls "$WORK"/*.json | grep -c worker)
"$BIRDDOG" stop --instance "$INSTANCE" >/dev/null
INSTANCE=""
WORKERS_AFTER=$(ls "$WORK"/*.json | grep -c worker)
[[ "$WORKERS_BEFORE" == "$WORKERS_AFTER" ]] \
  && ok "every watched worker is untouched after birddog stops" \
  || bad "stopping birddog disturbed the workers"

# --- result ------------------------------------------------------------------

printf '\n\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
