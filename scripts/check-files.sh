#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

errors=()
skip_path() {
  case "$1" in
    .git/*|.omx/*|*/__pycache__/*) return 0 ;;
    *) return 1 ;;
  esac
}

is_text_file() {
  local path="$1" base suffix
  base="$(basename "$path")"
  suffix="${path##*.}"
  case "$base" in
    Makefile|.gitignore|.editorconfig|LICENSE|go.mod|go.sum) return 0 ;;
  esac
  case ".$suffix" in
    .go|.sh|.md|.yml|.yaml|.txt|.json|.toml|.conf|.service) return 0 ;;
    *) return 1 ;;
  esac
}

check_exec() {
  local path="$1"
  if [[ -e "$path" && ! -x "$path" ]]; then
    errors+=("$path: should be executable")
  fi
}

while IFS= read -r -d '' path; do
  rel="${path#./}"
  skip_path "$rel" && continue
  [[ -f "$path" ]] || continue
  is_text_file "$rel" || continue

  if [[ -s "$path" ]]; then
    last_byte="$(tail -c 1 "$path" | od -An -t x1 | tr -d ' \n')"
    if [[ "$last_byte" != "0a" ]]; then
      errors+=("$rel: missing final newline")
    fi
  fi
  if command -v iconv >/dev/null 2>&1 && ! iconv -f UTF-8 -t UTF-8 "$path" >/dev/null 2>&1; then
    errors+=("$rel: not utf-8")
  fi
  while IFS=: read -r line_no _; do
    [[ -n "${line_no:-}" ]] || continue
    errors+=("$rel:$line_no: trailing whitespace")
  done < <(grep -nE '[[:blank:]]$' "$path" || true)
done < <(find . -type f -print0)

for path in \
  relaypilot.sh \
  install-relaypilot.sh \
  scripts/test.sh \
  scripts/build-release.sh \
  scripts/smoke-agent.sh \
  scripts/install-local-go.sh \
  scripts/check-files.sh
do
  check_exec "$path"
done

if ((${#errors[@]} > 0)); then
  printf '%s\n' "${errors[@]}" >&2
  exit 1
fi
echo "file checks: OK"
