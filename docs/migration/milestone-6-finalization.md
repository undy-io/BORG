# Milestone 6: Python Cleanup And Finalization (Completed)

## Status

Completed with BORG `v0.2.0` on July 28, 2026.

Go is the only active runtime. Runtime, deployment, validation, and release
flows no longer depend on Python or migration fallback code.

## Completed Checkpoints

### Runtime Removal

- [x] Remove the Python BORG runtime, `genkey.py`, and `entrypoint.sh`.
- [x] Remove Python packaging, lockfiles, runtime tests, and parity smoke tests.
- [x] Remove Python and UV assumptions from the active development workflow.
- [x] Retain the Python contract documents as explicitly historical records.

### Go-Native Validation

- [x] Keep package and process-level Go tests under `internal/` and
  `tests/k8s_smoke`.
- [x] Keep the Go `dummy-openai` backend for integration validation.
- [x] Run smoke coverage through the normal `go test ./...` path.
- [x] Complete local race validation and add CI-enforced tests, vet, pinned
  `golangci-lint`, and command builds.
- [x] Validate the Helm chart with strict lint and its render matrix.

### Deployment And Release Cutover

- [x] Pass `.github/workflows/docker.yml` against the root Go Dockerfile,
  satisfying the root-image build gate.
- [x] Deploy the Go runtime by default from the Helm chart.
- [x] Publish the runtime image and chart through the release workflows.
- [x] Release BORG `v0.2.0`.

### Host Acceptance

- [x] Run `scripts/validate-kind-go.sh --create-cluster --delete-cluster` from
  raw WSL/host.
- [x] Build and load the BORG and dummy backend images.
- [x] Validate root health, model discovery, missing-auth rejection,
  authenticated forwarding, auth replacement, and SSE streaming.
- [x] Validate that a config-only Helm upgrade changes the pod-template
  checksum and creates a new ReplicaSet.
- [x] Delete the KinD cluster after the successful run.

### Documentation Closeout

- [x] Archive the completed roadmap and milestone under `docs/migration`.
- [x] Remove obsolete session-recovery instructions.
- [x] Move active architecture and testing documentation out of the migration
  directory.
- [x] Leave future feature planning to a separate BORG product roadmap.

## Final Validation Baseline

The final local quality baseline is:

```bash
go test ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
helm lint --strict charts/borg
bash scripts/validate-helm-chart.sh
bash -n scripts/validate-kind-go.sh
git diff --check
```

Docker and real-cluster validation are recorded from the green GitHub workflows
and the successful July 28, 2026 host KinD acceptance run. They are not required
again for this documentation-only archive change.
