#!/usr/bin/env bash
# Verify release archive contents and, when possible, execute the native build.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: release-smoke.sh --dist DIR --version VERSION --commit COMMIT [--structure-only]

Checks all supported release archives and checksums. Unless --structure-only is
set, the archive matching the current runner is also executed to verify both
`mcplexer version --json` and the MCP initialize serverInfo.version response.
EOF
}

die() {
  echo "release smoke: $*" >&2
  exit 1
}

DIST_DIR=""
VERSION=""
COMMIT=""
STRUCTURE_ONLY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dist)
      [[ $# -ge 2 ]] || die "--dist requires a value"
      DIST_DIR="$2"
      shift 2
      ;;
    --version)
      [[ $# -ge 2 ]] || die "--version requires a value"
      VERSION="$2"
      shift 2
      ;;
    --commit)
      [[ $# -ge 2 ]] || die "--commit requires a value"
      COMMIT="$2"
      shift 2
      ;;
    --structure-only)
      STRUCTURE_ONLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n "$DIST_DIR" ]] || die "--dist is required"
[[ -d "$DIST_DIR" ]] || die "archive directory does not exist: $DIST_DIR"
[[ -n "$VERSION" ]] || die "--version is required"
[[ "$COMMIT" =~ ^[0-9a-fA-F]{7,40}$ ]] || die "--commit must be a 7-40 character hexadecimal revision"
[[ -f "$DIST_DIR/checksums.txt" ]] || die "missing $DIST_DIR/checksums.txt"

DIST_DIR="$(cd "$DIST_DIR" && pwd)"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

expected_archives() {
  printf '%s\n' \
    "mcplexer_${VERSION}_darwin_amd64.tar.gz" \
    "mcplexer_${VERSION}_darwin_arm64.tar.gz" \
    "mcplexer_${VERSION}_linux_amd64.tar.gz" \
    "mcplexer_${VERSION}_linux_arm64.tar.gz" \
    "mcplexer_${VERSION}_windows_amd64.zip" \
    "mcplexer_${VERSION}_windows_arm64.zip"
}

expected_payloads() {
  expected_archives
  local sbom="mcplexer_${VERSION}.spdx.json"
  if [[ -f "$DIST_DIR/$sbom" ]]; then
    [[ -s "$DIST_DIR/$sbom" ]] || die "SBOM is empty: $sbom"
    printf '%s\n' "$sbom"
  fi
}

expected_archives > "$TMP_ROOT/expected-archives"
while IFS= read -r archive; do
  [[ -f "$DIST_DIR/$archive" ]] || die "missing archive: $archive"
done < "$TMP_ROOT/expected-archives"

expected_payloads > "$TMP_ROOT/expected-payloads"
cp "$TMP_ROOT/expected-payloads" "$TMP_ROOT/expected-files"
printf '%s\n' checksums.txt >> "$TMP_ROOT/expected-files"
unexpected_payload="$(find "$DIST_DIR" -mindepth 1 -maxdepth 1 ! -type f -print -quit)"
[[ -z "$unexpected_payload" ]] || die "unexpected non-file release payload: $unexpected_payload"
find "$DIST_DIR" -mindepth 1 -maxdepth 1 -type f -exec basename {} \; \
  | LC_ALL=C sort > "$TMP_ROOT/actual-files"
LC_ALL=C sort "$TMP_ROOT/expected-files" > "$TMP_ROOT/expected-files-sorted"
if ! cmp -s "$TMP_ROOT/expected-files-sorted" "$TMP_ROOT/actual-files"; then
  echo "Expected release files:" >&2
  cat "$TMP_ROOT/expected-files-sorted" >&2
  echo "Actual release files:" >&2
  cat "$TMP_ROOT/actual-files" >&2
  die "release directory contains missing or unexpected payloads"
fi

awk 'NF == 2 { name=$2; sub(/^\*/, "", name); sub(/^\.\//, "", name); print name }' \
  "$DIST_DIR/checksums.txt" | LC_ALL=C sort > "$TMP_ROOT/checksum-payloads"
LC_ALL=C sort "$TMP_ROOT/expected-payloads" > "$TMP_ROOT/expected-payloads-sorted"
if ! cmp -s "$TMP_ROOT/expected-payloads-sorted" "$TMP_ROOT/checksum-payloads"; then
  echo "Expected checksum entries:" >&2
  cat "$TMP_ROOT/expected-payloads-sorted" >&2
  echo "Actual checksum entries:" >&2
  cat "$TMP_ROOT/checksum-payloads" >&2
  die "checksums.txt does not match the release payload set"
fi

echo "==> Verifying checksums"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$DIST_DIR" && sha256sum --check checksums.txt)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$DIST_DIR" && shasum -a 256 --check checksums.txt)
else
  die "sha256sum or shasum is required"
fi

inspect_archive() {
  local archive="$1"
  local platform="$2"
  local architecture="$3"
  local root="mcplexer_${VERSION}_${platform}_${architecture}"
  local listing="$TMP_ROOT/${platform}-${architecture}.listing"
  local binary="mcplexer"

  if [[ "$platform" == "windows" ]]; then
    binary="mcplexer.exe"
    command -v unzip >/dev/null 2>&1 || die "unzip is required to inspect $archive"
    unzip -Z1 "$DIST_DIR/$archive" > "$listing"
  else
    tar -tzf "$DIST_DIR/$archive" > "$listing"
  fi

  while IFS= read -r path; do
    case "$path" in
      "$root"|"$root/"|"$root/"*) ;;
      *) die "$archive contains a path outside its package root: $path" ;;
    esac
    case "$path" in
      /*|*/../*|../*|*/..) die "$archive contains an unsafe path: $path" ;;
    esac
  done < "$listing"

  local required
  for required in \
    "$binary" \
    README.md \
    LICENSE \
    NOTICE \
    THIRD_PARTY_NOTICES.md \
    THIRD_PARTY_LICENSES/README.md \
    THIRD_PARTY_LICENSES/INDEX.tsv \
    INSTALL.txt \
    install-release.sh \
    install-release.ps1; do
    grep -Fqx "$root/$required" "$listing" || die "$archive is missing $root/$required"
  done
  grep -Fq "$root/THIRD_PARTY_LICENSES/go/" "$listing" \
    || die "$archive contains no Go dependency license files"
  grep -Fq "$root/THIRD_PARTY_LICENSES/npm/" "$listing" \
    || die "$archive contains no npm dependency metadata"
}

echo "==> Inspecting archive contents"
inspect_archive "mcplexer_${VERSION}_darwin_amd64.tar.gz" darwin amd64
inspect_archive "mcplexer_${VERSION}_darwin_arm64.tar.gz" darwin arm64
inspect_archive "mcplexer_${VERSION}_linux_amd64.tar.gz" linux amd64
inspect_archive "mcplexer_${VERSION}_linux_arm64.tar.gz" linux arm64
inspect_archive "mcplexer_${VERSION}_windows_amd64.zip" windows amd64
inspect_archive "mcplexer_${VERSION}_windows_arm64.zip" windows arm64

if [[ "$STRUCTURE_ONLY" -eq 1 ]]; then
  echo "release archive structure smoke passed"
  exit 0
fi

case "$(uname -s)" in
  Darwin) HOST_OS="darwin" ;;
  Linux) HOST_OS="linux" ;;
  MINGW*|MSYS*|CYGWIN*) HOST_OS="windows" ;;
  *) die "unsupported smoke-test host OS: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH="amd64" ;;
  arm64|aarch64) HOST_ARCH="arm64" ;;
  *) die "unsupported smoke-test host architecture: $(uname -m)" ;;
esac

NATIVE_ROOT="mcplexer_${VERSION}_${HOST_OS}_${HOST_ARCH}"
if [[ "$HOST_OS" == "windows" ]]; then
  NATIVE_ARCHIVE="${NATIVE_ROOT}.zip"
  NATIVE_BINARY="$TMP_ROOT/native/$NATIVE_ROOT/mcplexer.exe"
  mkdir -p "$TMP_ROOT/native"
  unzip -q "$DIST_DIR/$NATIVE_ARCHIVE" -d "$TMP_ROOT/native"
else
  NATIVE_ARCHIVE="${NATIVE_ROOT}.tar.gz"
  NATIVE_BINARY="$TMP_ROOT/native/$NATIVE_ROOT/mcplexer"
  mkdir -p "$TMP_ROOT/native"
  tar -xzf "$DIST_DIR/$NATIVE_ARCHIVE" -C "$TMP_ROOT/native"
fi

[[ -f "$NATIVE_BINARY" ]] || die "native archive did not extract its binary: $NATIVE_ARCHIVE"
if [[ "$HOST_OS" != "windows" ]]; then
  [[ -x "$NATIVE_BINARY" ]] || die "native binary is not executable: $NATIVE_ARCHIVE"
fi

if command -v python3 >/dev/null 2>&1; then
  PYTHON=python3
elif command -v python >/dev/null 2>&1; then
  PYTHON=python
else
  die "Python 3 is required for native release smoke tests"
fi

echo "==> Executing native archive: $NATIVE_ARCHIVE"
"$PYTHON" - "$NATIVE_BINARY" "$VERSION" "$COMMIT" "$HOST_OS" "$HOST_ARCH" "$TMP_ROOT/runtime-home" <<'PY'
import json
import os
import pathlib
import subprocess
import sys
import threading

binary, version, commit, expected_os, expected_arch, runtime_home = sys.argv[1:]
short_commit = commit[:12]
shortest_commit = commit[:7]
expected_cli_version = version
if commit not in version and short_commit not in version and shortest_commit not in version:
    expected_cli_version = f"{version}+{short_commit}"

version_result = subprocess.run(
    [binary, "version", "--json"],
    check=False,
    capture_output=True,
    text=True,
    timeout=30,
)
if version_result.returncode != 0:
    raise SystemExit(
        f"version command failed ({version_result.returncode}): {version_result.stderr.strip()}"
    )
try:
    version_info = json.loads(version_result.stdout)
except json.JSONDecodeError as exc:
    raise SystemExit(f"version command returned invalid JSON: {exc}") from exc

expected = {
    "version": expected_cli_version,
    "commit": short_commit,
    "goos": expected_os,
    "goarch": expected_arch,
    "p2p": True,
}
for field, value in expected.items():
    if version_info.get(field) != value:
        raise SystemExit(
            f"version JSON {field}={version_info.get(field)!r}, expected {value!r}"
        )

home = pathlib.Path(runtime_home)
home.mkdir(parents=True, exist_ok=True)
env = os.environ.copy()
env.update(
    {
        "HOME": str(home),
        "USERPROFILE": str(home),
        "APPDATA": str(home / "AppData" / "Roaming"),
        "LOCALAPPDATA": str(home / "AppData" / "Local"),
        "MCPLEXER_NO_PROXY": "1",
    }
)
request = {
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "release-smoke", "version": "1.0.0"},
    },
}
initialize_process = subprocess.Popen(
    [binary, "serve", "--mode=stdio"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
    bufsize=1,
    cwd=home,
    env=env,
)
if initialize_process.stdin is None or initialize_process.stdout is None:
    initialize_process.kill()
    raise SystemExit("MCP initialize process did not expose stdio pipes")

# Keep stdin open until the response arrives. subprocess.run(input=...) closes
# it immediately after writing, which correctly tells a stdio MCP server that
# its client disconnected and can cancel an in-flight initialize request.
initialize_process.stdin.write(json.dumps(request) + "\n")
initialize_process.stdin.flush()

timed_out = threading.Event()


def kill_on_timeout():
    timed_out.set()
    try:
        initialize_process.kill()
    except OSError:
        pass


timer = threading.Timer(60, kill_on_timeout)
timer.daemon = True
timer.start()
response = None
stdout_lines = []
try:
    for line in initialize_process.stdout:
        stdout_lines.append(line)
        if not line.strip():
            continue
        try:
            candidate = json.loads(line)
        except json.JSONDecodeError:
            continue
        if candidate.get("id") == 1:
            response = candidate
            break
finally:
    timer.cancel()
    try:
        initialize_process.stdin.close()
    except (BrokenPipeError, OSError):
        pass
    # communicate() otherwise tries to flush the pipe we intentionally closed.
    initialize_process.stdin = None

try:
    stdout_tail, initialize_stderr = initialize_process.communicate(timeout=30)
except subprocess.TimeoutExpired as exc:
    initialize_process.kill()
    stdout_tail, initialize_stderr = initialize_process.communicate()
    raise SystemExit("MCP initialize process did not exit after stdin closed") from exc

initialize_stdout = "".join(stdout_lines) + (stdout_tail or "")
if timed_out.is_set():
    detail = (initialize_stderr or initialize_stdout).strip()
    raise SystemExit(f"MCP initialize smoke timed out after 60 seconds: {detail}")
if initialize_process.returncode != 0:
    detail = (initialize_stderr or initialize_stdout).strip()
    raise SystemExit(
        "MCP initialize process failed "
        f"({initialize_process.returncode}): {detail}"
    )

if response is None:
    raise SystemExit("MCP initialize produced no JSON-RPC response for request id 1")
if "error" in response:
    raise SystemExit(f"MCP initialize returned an error: {response['error']!r}")
server_version = response.get("result", {}).get("serverInfo", {}).get("version")
if server_version != version:
    raise SystemExit(
        f"MCP serverInfo.version={server_version!r}, expected {version!r}"
    )

print(
    f"native release smoke passed: CLI {expected_cli_version}; MCP server {server_version}"
)
PY
