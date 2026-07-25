#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

manifest=$(mktemp)
trap 'rm -f "$manifest"' EXIT
git ls-files -z -- '*.go' >"$manifest"
mapfile -d '' -t go_files <"$manifest"
if ((${#go_files[@]} == 0)); then
  echo "gofmt: git enumerated zero tracked Go files" >&2
  exit 1
fi
printf 'gofmt: checking %d tracked Go file(s)\n' "${#go_files[@]}"

unformatted=$(gofmt -l "${go_files[@]}")
if [[ -n "$unformatted" ]]; then
  echo "gofmt: unformatted tracked Go files:" >&2
  printf '%s\n' "$unformatted" >&2
  exit 1
fi
