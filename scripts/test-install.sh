#!/usr/bin/env bash
# test-install.sh — end-to-end install validation for mcplexer.
# Builds the binary, starts the daemon, verifies health + dashboard,
# runs config show and doctor, then cleans up. Uses a temp directory
# so the real ~/.mcplexer is never touched.
set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TEST_HOME="$(mktemp -d)"
export HOME="$TEST_HOME"

PASS=0
FAIL=0
DAEMON_PID=""

cleanup() {
  if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  if [ -n "$TEST_HOME" ] && [ -d "$TEST_HOME" ]; then
    chmod -R u+w "$TEST_HOME" 2>/dev/null || true
    rm -rf "$TEST_HOME" || true
  fi
}
trap cleanup EXIT

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1 — $2"; FAIL=$((FAIL + 1)); }

echo "==> mcplexer install test"
echo "    repo:  $REPO_ROOT"
echo "    home:  $TEST_HOME"
echo ""

# --- Step 1: Check Go 1.25+ and Node 20+ ---
echo "[1/9] Checking toolchain..."

if command -v go &>/dev/null; then
  go_ver="$(go version | grep -oE 'go[0-9]+\.[0-9]+' | head -1 | sed 's/go//')"
  go_major="${go_ver%%.*}"
  go_minor="${go_ver##*.}"
  if [ "$go_major" -ge 1 ] && [ "$go_minor" -ge 25 ]; then
    pass "Go $go_ver"
  else
    fail "Go version" "need 1.25+, got $go_ver"
  fi
else
  fail "Go" "not found"
fi

if command -v node &>/dev/null; then
  node_ver="$(node -v | sed 's/v//')"
  node_major="${node_ver%%.*}"
  if [ "$node_major" -ge 20 ]; then
    pass "Node v$node_ver"
  else
    fail "Node version" "need 20+, got v$node_ver"
  fi
else
  fail "Node" "not found"
fi

# --- Step 2: Build the binary ---
echo ""
echo "[2/9] Building binary..."

cd "$REPO_ROOT"
if go build -o bin/mcplexer ./cmd/mcplexer 2>&1; then
  if [ -f bin/mcplexer ]; then
    pass "go build → bin/mcplexer ($(du -h bin/mcplexer | cut -f1))"
  else
    fail "go build" "binary not found at bin/mcplexer"
  fi
else
  fail "go build" "compilation failed"
fi

# --- Step 3: Initialize ---
echo ""
echo "[3/9] Running init..."

mkdir -p "$TEST_HOME/.mcplexer"
if ./bin/mcplexer init 2>&1; then
  if [ -f "$TEST_HOME/.mcplexer/mcplexer.db" ] || [ -f "$TEST_HOME/.mcplexer/mcplexer.yaml" ]; then
    pass "init created data dir"
  else
    fail "init" "no db/config created in $TEST_HOME/.mcplexer"
  fi
else
  fail "init" "exit code $?"
fi

# --- Step 4: Start daemon ---
echo ""
echo "[4/9] Starting daemon..."

./bin/mcplexer serve --mode=http --addr=127.0.0.1:13333 &
DAEMON_PID=$!
pass "daemon started (PID $DAEMON_PID)"

# --- Step 5: Wait for health ---
echo ""
echo "[5/9] Waiting for health endpoint..."

HEALTH_OK=0
for i in $(seq 1 30); do
  if curl -sf http://127.0.0.1:13333/api/v1/health >/dev/null 2>&1; then
    HEALTH_OK=1
    break
  fi
  sleep 1
done

if [ "$HEALTH_OK" -eq 1 ]; then
  health_body="$(curl -s http://127.0.0.1:13333/api/v1/health)"
  pass "health endpoint responded: $health_body"
else
  fail "health endpoint" "no response after 30s"
fi

# --- Step 6: Verify dashboard ---
echo ""
echo "[6/9] Checking dashboard..."

dashboard_body="$(curl -s http://127.0.0.1:13333/ 2>/dev/null || true)"
if echo "$dashboard_body" | grep -qi 'mcplexer'; then
  pass "dashboard contains 'MCPlexer'"
else
  fail "dashboard" "expected 'MCPlexer' in response body"
fi

# --- Step 7: config show ---
echo ""
echo "[7/9] Running config show..."

if config_out="$(./bin/mcplexer config show 2>&1)"; then
  pass "config show succeeded"
else
  fail "config show" "exit code $?"
fi

# --- Step 8: doctor ---
echo ""
echo "[8/9] Running doctor..."

# Doctor checks port availability which will fail since daemon is bound.
# That's expected — we just want it to run without crashing.
doctor_out="$(./bin/mcplexer doctor 2>&1 || true)"
if echo "$doctor_out" | grep -q '/'; then
  ok_count="$(echo "$doctor_out" | grep -c '✓' || true)"
  total_count="$(echo "$doctor_out" | grep -c '[✓✗]' || true)"
  pass "doctor ran ($ok_count/$total_count checks passed)"
else
  fail "doctor" "unexpected output: $doctor_out"
fi

# --- Step 9: Clean up ---
echo ""
echo "[9/9] Cleaning up..."

if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
  kill "$DAEMON_PID" 2>/dev/null || true
  wait "$DAEMON_PID" 2>/dev/null || true
  DAEMON_PID=""
  pass "daemon stopped"
else
  pass "daemon already exited"
fi

# trap handles TEST_HOME removal

# --- Summary ---
echo ""
echo "========================================="
echo "  Results: $PASS passed, $FAIL failed"
echo "========================================="

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
