#!/usr/bin/env bash
set -euo pipefail

if [[ -f pyproject.toml ]]; then
  uv sync --frozen
  if uv run python -c "import playwright" >/dev/null 2>&1; then
    uv run playwright install chromium
  fi
fi

if command -v go >/dev/null 2>&1; then
  export GOBIN=/usr/local/bin
  go install golang.org/x/tools/gopls@v0.21.1
  go install golang.org/x/tools/cmd/goimports@v0.44.0
  go install github.com/go-delve/delve/cmd/dlv@v1.26.1
fi
