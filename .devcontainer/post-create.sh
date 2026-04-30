#!/usr/bin/env bash
set -euo pipefail

if command -v go >/dev/null 2>&1; then
  export GOBIN=/usr/local/bin
  go install golang.org/x/tools/gopls@v0.21.1
  go install golang.org/x/tools/cmd/goimports@v0.44.0
  go install github.com/go-delve/delve/cmd/dlv@v1.26.1
  go install sigs.k8s.io/kind@v0.31.0
fi
