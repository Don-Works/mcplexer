#!/usr/bin/env bash
# Collect the license and notice files shipped by release dependencies.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="${1:-}"

if [[ -z "$DEST" ]]; then
  echo "usage: generate-third-party-licenses.sh DEST" >&2
  exit 2
fi
if [[ "$DEST" != /* ]]; then
  DEST="$ROOT/$DEST"
fi

command -v go >/dev/null 2>&1 || {
  echo "go is required to collect Go dependency licenses" >&2
  exit 1
}
command -v node >/dev/null 2>&1 || {
  echo "node is required to collect npm dependency licenses" >&2
  exit 1
}
[[ -d "$ROOT/web/node_modules" ]] || {
  echo "web/node_modules is missing; run npm ci before collecting licenses" >&2
  exit 1
}

rm -rf "$DEST"
mkdir -p "$DEST/go" "$DEST/npm"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

INDEX="$DEST/INDEX.tsv"
{
  printf 'ecosystem\tpackage\tversion\tdeclared_license\tfiles\n'
} > "$INDEX"

license_files() {
  local dir="$1"
  find "$dir" -maxdepth 3 -type f \
    ! -path '*/node_modules/*' \
    \( -iname 'license*' -o -iname 'licence*' -o -iname 'copying*' \
       -o -iname 'notice*' -o -iname 'copyright*' -o -iname 'patents*' \) \
    -print0
}

copy_license_files() {
  local source_dir="$1"
  local output_dir="$2"
  local copied=0
  local source rel target
  local listed=""

  while IFS= read -r -d '' source; do
    rel="${source#"$source_dir"/}"
    target="$output_dir/$rel"
    mkdir -p "$(dirname "$target")"
    cp "$source" "$target"
    if [[ -n "$listed" ]]; then
      listed+="," 
    fi
    listed+="$rel"
    copied=$((copied + 1))
  done < <(license_files "$source_dir" | LC_ALL=C sort -z)

  [[ "$copied" -gt 0 ]] || return 1
  printf '%s' "$listed"
}

echo "==> Collecting Go module licenses"
(cd "$ROOT" && go list -deps -tags p2p \
  -f '{{if and .Module (not .Module.Main)}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}' \
  ./cmd/mcplexer) | LC_ALL=C sort -u > "$TMP_ROOT/go-modules"
while IFS='|' read -r module version module_dir; do
  [[ -n "$module" && -n "$module_dir" ]] || continue
  package_dir="$DEST/go/${module}@${version:-unknown}"
  mkdir -p "$package_dir"
  if files="$(copy_license_files "$module_dir" "$package_dir")"; then
    printf 'go\t%s\t%s\t\t%s\n' "$module" "${version:-unknown}" "$files" >> "$INDEX"
  else
    echo "no license or notice file found for Go module $module ${version:-unknown}" >&2
    exit 1
  fi
done < "$TMP_ROOT/go-modules"

echo "==> Collecting production npm package licenses"
(cd "$ROOT/web" && npm ls --omit=dev --all --parseable) \
  | tail -n +2 | LC_ALL=C sort -u > "$TMP_ROOT/npm-packages"
while IFS= read -r package_dir; do
  [[ -f "$package_dir/package.json" ]] || continue
  metadata="$(node -e '
    const p = require(process.argv[1]);
    const clean = (v) => String(v || "unknown").replace(/[\t\r\n]/g, " ");
    process.stdout.write([clean(p.name), clean(p.version), clean(p.license)].join("\t"));
  ' "$package_dir/package.json")"
  IFS=$'\t' read -r package version declared_license <<< "$metadata"
  [[ -n "$package" ]] || continue

  output_dir="$DEST/npm/${package}@${version:-unknown}"
  if [[ -f "$output_dir/package.json" ]]; then
    continue
  fi
  mkdir -p "$output_dir"
  cp "$package_dir/package.json" "$output_dir/package.json"
  files="package.json"
  if copied="$(copy_license_files "$package_dir" "$output_dir")"; then
    files+=",$copied"
  elif [[ -z "$declared_license" || "$declared_license" == "unknown" ]]; then
    echo "npm package $package ${version:-unknown} has neither license files nor declared license metadata" >&2
    exit 1
  fi
  printf 'npm\t%s\t%s\t%s\t%s\n' \
    "$package" "${version:-unknown}" "${declared_license:-unknown}" "$files" >> "$INDEX"
done < "$TMP_ROOT/npm-packages"

{
  printf '# Third-party license bundle\n\n'
  printf 'This directory is generated from the exact Go dependency graph and production npm tree used by the MCPlexer release build.\n\n'
  printf -- '- `INDEX.tsv` maps every dependency to the copied license/notice files.\n'
  printf -- '- `go/` contains verbatim files from Go module distributions.\n'
  printf -- '- `npm/` contains package metadata plus verbatim license/notice files when the published package includes them.\n\n'
  printf 'A package that declares a license in `package.json` but omits a separate license file is retained with its complete package metadata.\n'
} > "$DEST/README.md"

LC_ALL=C sort -o "$INDEX" "$INDEX"
printf 'third-party licenses: %s dependencies\n' "$(( $(wc -l < "$INDEX") - 1 ))"
