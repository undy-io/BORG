#!/usr/bin/env bash
set -euo pipefail

if [[ -f pyproject.toml ]]; then
  uv sync --frozen
  if uv run python -c "import playwright" >/dev/null 2>&1; then
    uv run playwright install chromium
  fi
fi
