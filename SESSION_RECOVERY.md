# BORG Session Recovery

Use this file to resume the migration work if chat history is lost.

## Project Goal
Migrate BORG from Python to Go using a side-by-side approach, with Python remaining the reference implementation until the Go version reaches parity and is ready for cutover.

## Decisions Already Made
- We are not doing an in-place rewrite.
- We will work milestone-by-milestone.
- High-level milestones live in [ROADMAP.md](/workspaces/borg/ROADMAP.md).
- The active milestone will be expanded into a repo-root `MILESTONE.md`.
- The devcontainer should support both Python and Go during migration.
- The devcontainer should not wait until final cutover to gain Go support.

## Baseline Status
- The Python service is small and a good Go migration candidate.
- Main implementation areas:
  - [src/borg/main.py](/workspaces/borg/src/borg/main.py)
  - [src/borg/proxy.py](/workspaces/borg/src/borg/proxy.py)
  - [src/borg/k8s_discovery.py](/workspaces/borg/src/borg/k8s_discovery.py)
  - [genkey.py](/workspaces/borg/genkey.py)
- Baseline test result observed during planning:
  - `uv run pytest -q`
  - `35 passed in 13.15s`

## Devcontainer Changes Added
- Added Go to the devcontainer via the Dev Containers Go feature in [.devcontainer/devcontainer.json](/workspaces/borg/.devcontainer/devcontainer.json).
- Added the VS Code Go extension and basic Go formatting settings.
- Updated [.devcontainer/post-create.sh](/workspaces/borg/.devcontainer/post-create.sh) to install:
  - `gopls`
  - `goimports`
  - `dlv`
- Pinned the migration environment to explicit Go and tool versions:
  - Go feature `ghcr.io/devcontainers/features/go:1.3.3`
  - Go toolchain `1.26.2`
  - `gopls@v0.21.1`
  - `goimports@v0.44.0`
  - `dlv@v1.26.1`

## Expected Next Step
Rebuild/reopen the devcontainer so the Go toolchain and tools are installed.

After rebuild, verify:
- `go version`
- `which gopls`
- `which goimports`
- `which dlv`

## Likely Next Planning Step
Create the first repo-root `MILESTONE.md` for Milestone 1 in [ROADMAP.md](/workspaces/borg/ROADMAP.md), focused on freezing the Python contract and defining Go parity targets.

## Current Workflow Expectation
- Keep Python working.
- Add Go beside it.
- Do not remove Python runtime, CI, or deployment path early.
- Prefer parity over optimization.

## Local Repo Context
There were already unrelated local changes in the worktree before this step. Do not revert them unless explicitly asked.

Observed pre-existing modified/untracked items included:
- `charts/borg/templates/deployment.yaml`
- `charts/borg/templates/secret.yaml`
- `charts/borg/values.schema.json`
- `charts/borg/values.yaml`
- `genkey.py`
- `src/borg/main.py`
- `tests/test_genkey.py`
- `.codex`

## Resume Prompt
If needed, start a new chat with something like:

`Please resume the BORG Go migration from SESSION_RECOVERY.md and ROADMAP.md. The devcontainer was just updated to support Go, and the next step is to rebuild it and then create MILESTONE.md for Milestone 1.`
