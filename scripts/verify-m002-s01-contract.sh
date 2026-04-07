#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

failures=0

require_heading() {
  local file=$1
  local heading=$2
  if ! grep -Fq "$heading" "$file"; then
    echo "missing heading in $file: $heading" >&2
    failures=$((failures + 1))
  fi
}

forbid_pattern() {
  local file=$1
  local pattern=$2
  local note=$3
  if grep -En "$pattern" "$file" >/dev/null; then
    echo "stale contract phrase in $file: $note" >&2
    grep -En "$pattern" "$file" >&2 || true
    failures=$((failures + 1))
  fi
}

DOC=docs/design/contract-convergence.md
if [[ ! -f "$DOC" ]]; then
  echo "missing authority map: $DOC" >&2
  exit 1
fi

require_heading "$DOC" "## Authority Map"
require_heading "$DOC" "## Bootstrap Contract"
require_heading "$DOC" "## State Mapping"
require_heading "$DOC" "## Security Boundaries"
require_heading "$DOC" "## Shim Target Contract"

# Legacy wording this slice is meant to retire from normative docs.
forbid_pattern docs/design/runtime/config-spec.md '作为第一个 prompt 发送给 agent|静默发送' 'systemPrompt treated as a seed prompt instead of bootstrap/session configuration'
forbid_pattern docs/design/runtime/runtime-spec.md 'If `acpAgent\.systemPrompt` is non-empty, the runtime MUST then send it as the first|seed prompt' 'runtime spec still treats systemPrompt as a follow-up prompt'
forbid_pattern docs/design/agentd/ari-spec.md 'acpAgent\.session\.cwd|路径写入 `acpAgent\.session\.cwd`' 'ARI still describes cwd as an input field instead of a runtime-derived resolved path'
forbid_pattern docs/design/runtime/shim-rpc-spec.md '"method": "Prompt"|"method": "Cancel"|"method": "Subscribe"|"method": "GetState"|"method": "GetHistory"|"method": "Shutdown"|"method": "\$/event"' 'shim spec still presents the legacy PascalCase/$/event surface as normative'

if (( failures > 0 )); then
  echo "contract verification failed with $failures issue(s)" >&2
  exit 1
fi

echo "contract verification passed"
