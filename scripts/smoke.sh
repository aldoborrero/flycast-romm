#!/usr/bin/env bash
# Exercise the whole broker contract against a live container.
#
# The unit tests cover everything provable without an emulator. This covers the
# rest: that Flycast actually launches, that the Lua command channel answers,
# that a savestate lands on disk, and that pactl reaches abc's PulseAudio.
#
#   STREAMING_BROKER_SECRET=... ROM=dc/game.chd scripts/smoke.sh
#
# ROM is relative to ROM_ROOT inside the container. Nothing here is destructive
# beyond writing save slots 1 and 10 for that game.
set -euo pipefail

BROKER="${BROKER:-http://localhost:8000}"
SECRET="${STREAMING_BROKER_SECRET:-${BROKER_SECRET:-}}"
ROM_ROOT="${ROM_ROOT:-/romm/library/roms}"
ROM="${ROM:-}"
COMPOSE="${COMPOSE:-deploy/docker-compose.yml}"
KEEP_UP="${KEEP_UP:-0}"

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() {
  printf '\033[31mFAIL: %s\033[0m\n' "$*" >&2
  exit 1
}
ok() { printf '  ok: %s\n' "$*"; }

req() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -o /tmp/smoke.out -w '%{http_code}' -X "$method" "${BROKER}${path}")
  [ -n "$SECRET" ] && args+=(-H "X-Broker-Secret: ${SECRET}")
  if [ -n "$body" ]; then
    args+=(-H 'Content-Type: application/json' -d "$body")
  fi
  curl "${args[@]}"
}

# expect <code> <method> <path> [body] -- asserts the status and shows the body
expect() {
  local want=$1 method=$2 path=$3 body=${4:-}
  local got
  got=$(req "$method" "$path" "$body")
  if [ "$got" != "$want" ]; then
    printf '  %s %s -> %s (wanted %s)\n' "$method" "$path" "$got" "$want" >&2
    cat /tmp/smoke.out >&2
    echo >&2
    fail "$method $path"
  fi
  ok "$method $path -> $got $(head -c 200 /tmp/smoke.out)"
}

[ -n "$ROM" ] || fail "set ROM to a path under ROM_ROOT, e.g. ROM=dc/game.chd"

if [ "${SKIP_COMPOSE:-0}" != "1" ]; then
  step "Bringing up ${COMPOSE}"
  docker compose -f "$COMPOSE" up -d
  if [ "$KEEP_UP" != "1" ]; then
    trap 'docker compose -f "$COMPOSE" down' EXIT
  fi
fi

step "Waiting for the broker"
for i in $(seq 1 90); do
  if curl -fsS "${BROKER}/health" >/dev/null 2>&1; then break; fi
  [ "$i" = 90 ] && fail "the broker never answered /health"
  sleep 2
done

step "Health reports a usable container"
curl -sS "${BROKER}/health" >/tmp/smoke.health
cat /tmp/smoke.health
echo
grep -q '"status":"ok"' /tmp/smoke.health || fail "/health is not ok"
grep -q '"flycast_installed":true' /tmp/smoke.health || fail "flycast is not where the broker expects it"
grep -q '"display_up":true' /tmp/smoke.health || fail "no display: the compositor did not come up"
grep -q '"lua_ready":true' /tmp/smoke.health ||
  fail "the lua command channel never answered, so save and load states will not work"
ok "flycast, display and lua channel all present"

step "Auth"
if [ -n "$SECRET" ]; then
  code=$(curl -sS -o /dev/null -w '%{http_code}' "${BROKER}/status")
  [ "$code" = "403" ] || fail "GET /status without the secret returned $code, expected 403"
  ok "an unauthenticated request is refused"
fi
# /health is exempt so a container healthcheck can use it.
code=$(curl -sS -o /dev/null -w '%{http_code}' "${BROKER}/health")
[ "$code" = "200" ] || fail "GET /health without the secret returned $code"
ok "/health needs no secret"

step "Rejections happen before anything is launched"
expect 400 POST /launch '{"rom_path":"/etc/passwd"}'
expect 422 POST /launch "{\"rom_path\":\"${ROM_ROOT}/definitely-not-here.chd\"}"
expect 400 POST /launch '{}'
expect 409 POST /save-state '{"slot":1}'

step "Launch"
expect 200 POST /launch "{\"rom_path\":\"${ROM_ROOT}/${ROM}\",\"rom_name\":\"smoke test\"}"
grep -q '"ready":true' /tmp/smoke.out ||
  echo "  note: the emulator had not reported running before LAUNCH_WAIT; it may still be booting"
sleep 5
expect 200 GET /status
grep -q '"active":true' /tmp/smoke.out || fail "/status does not show a loaded game"

step "Volume and mute"
expect 200 POST /volume '{"level":40}'
expect 400 POST /volume '{"level":101}'
expect 200 POST /mute '{"mute":true}'
expect 200 POST /mute '{"mute":false}'
expect 200 POST /mute '{}'
expect 200 POST /mute '{}'

step "Save to a manual slot"
expect 200 POST /save-state '{"slot":1}'
grep -q '"status":"saving"' /tmp/smoke.out || fail "/save-state must answer the literal status RomM checks for"
# The save is confirmed on a background goroutine; wait for it to clear.
for i in $(seq 1 30); do
  req GET /status >/dev/null
  grep -q '"save_in_progress":false' /tmp/smoke.out && break
  [ "$i" = 30 ] && fail "the save never finished"
  sleep 1
done
ok "the save completed"

step "Load it back"
expect 200 POST /load-state '{"slot":1}'
grep -q '"loaded":true' /tmp/smoke.out || fail "/load-state did not confirm"

step "Save and exit"
expect 200 POST /save-and-exit '{"slot":0,"wait":true}'
grep -q '"saved":true' /tmp/smoke.out ||
  fail "save-and-exit reported saved=false: the state did not land before the emulator was killed"
sleep 3
expect 200 GET /status
grep -q '"active":false' /tmp/smoke.out || fail "the session survived save-and-exit"

step "Check the state files"
docker compose -f "$COMPOSE" exec -T flycast \
  ls -la /config/.local/share/flycast/ | grep -i '\.state' ||
  fail "no .state files were written"

step "Release"
expect 200 DELETE /launch

printf '\n\033[32mAll contract calls behaved.\033[0m\n'
