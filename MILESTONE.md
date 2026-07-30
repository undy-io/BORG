# Milestone: Request Event Logging And Kafka Export

## Status

Planned. This is the first product milestone after completion of the Go
migration.

## Objective

Add optional, filterable request-event export without changing proxy outcomes or
readiness. The default sink is `noop`; Kafka is the first external sink.

Operators must be able to select traffic by the authorized BORG principal,
model, and configured session headers, then reconstruct request and response
streams from ordered events. Export failures must never fail a model request,
block its execution, or trigger an additional upstream attempt.

## Decisions

- Add an `internal/requestlog` subsystem with a sink interface. Proxy and HTTP
  packages depend on the interface, not Kafka.
- Use `github.com/twmb/franz-go/pkg/kgo` for the Kafka producer. It is pure Go,
  preserves the existing no-CGO build, and supports the required TLS and SASL
  producer modes.
- Treat the value returned by `auth.Require` as the current API-key identity and
  call it `principal` in the event contract. With auth disabled it is
  `ANONYMOUS`.
- Never emit or match literal bearer tokens, the AES auth key, backend API keys,
  cookies, or authorization headers.
- Use versioned JSON events on one Kafka topic. Use a stable Kafka key so all
  events for a request, or configured session, share a partition.
- Cap every version 1 serialized event value at 262,144 bytes. Filtering and
  partitioning always use complete raw identity and header values; event fields
  use bounded representations described by the event contract.
- Export is best effort and fail-open. A shared record-and-byte budget bounds all
  events accepted by BORG but not yet completed by the sink. Events may be
  dropped when that budget is full or during an expired shutdown flush; request
  handling remains unaffected.
- Apply filters before copying request or response payload bytes. No matching
  rule means no capture. An explicit empty rule matches all successfully
  authorized, parseable proxy requests, including `ANONYMOUS` when auth is
  disabled.
- Capture bytes only after BORG accepts a request body or successfully writes a
  response chunk to the client. Sequence gaps make dropped events detectable.

## Runtime Configuration

Add the following under `borg`:

```yaml
request_logging:
  sink: noop # noop or kafka
  queue_capacity: 100000
  queue_capacity_bytes: 4294967296 # 4 GiB total outstanding Kafka key+value bytes
  shutdown_timeout_seconds: 10
  capture:
    request_body: true
    response_body: true
    max_request_body_bytes: 524288
    max_response_body_bytes: 16777216
  session_headers: []
  # - name: X-Session-ID
  #   value_mode: sha256 # sha256 or raw
  partition_header: ""
  filters: []
  # - principals: ["^team-a$"]
  #   models: ["^Qwen/"]
  #   headers:
  #     X-Session-ID: [".+"]
  kafka:
    brokers: []
    topic: borg.request-events.v1
    client_id: borg
    tls:
      enabled: false
      server_name: ""
      ca_file: ""
      cert_file: ""
      key_file: ""
    sasl:
      mechanism: none # none, plain, scram-sha-256, or scram-sha-512
      username_from_env: BORG_KAFKA_USERNAME
      password_from_env: BORG_KAFKA_PASSWORD
```

Configuration rules:

- `sink` defaults to `noop`; unknown sinks fail configuration resolution.
- Kafka requires at least one broker and a non-empty topic. Broker reachability
  is not a startup or readiness requirement.
- Queue record capacity, queue byte capacity, and shutdown timeout must be
  positive. Represent byte capacities and accounting with overflow-checked
  `int64` values.
- Queue limits apply to total Kafka `len(key)+len(value)` bytes and records from
  successful non-blocking enqueue until the sink's delivery callback completes.
  Acquiring either budget must never block a request. Release each reservation
  exactly once on delivery, terminal failure, or shutdown drop.
- At capacity, retain previously accepted records and reject newer events. The
  4 GiB default holds 16,384 events at the absolute event ceiling or roughly
  98,000 minimally decorated 32 KiB body-chunk events; the 100,000-record limit
  may bind first. Go, TLS, compression, and Kafka client overhead remain outside
  the byte budget, so Kafka deployments must set a higher container memory limit.
- Capture limits must be non-negative; `0` means unlimited. Body capture can be
  disabled independently.
- Filter strings use Go RE2 regular expressions and are compiled at startup.
  Invalid expressions fail configuration resolution.
- Rules are ORed. Within one rule, principal, model, and header constraints are
  ANDed; patterns within a field are ORed. An omitted field is unconstrained.
- Header names are case-insensitive. Every header used by a rule or as the
  partition header must be declared in `session_headers`. Reject duplicate
  declarations after lowercasing, and use lowercase header names in events and
  compiled rules.
- Reject these case-insensitive names from `session_headers`: `Authorization`,
  `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `Authentication-Info`,
  `Proxy-Authentication-Info`, `API-Key`, `X-API-Key`, `X-Auth-Token`,
  `X-Access-Token`, and `X-Amz-Security-Token`.
- Filtering evaluates raw header values. Events store each value according to
  `value_mode`; `sha256` is the default and emits lowercase hexadecimal prefixed
  with `sha256:`.
- Repeated values for one session header remain an ordered JSON array. A header
  constraint matches when any raw value matches. Hash each captured value
  independently.
- When present, `partition_header` uses the SHA-256 digest of an unambiguous,
  length-prefixed encoding of all its raw values as the Kafka key regardless of
  capture mode. Otherwise the request ID is the key.
- TLS uses system roots when `ca_file` is empty. Client certificate and key must
  be configured together.
- SASL credentials are resolved only from the named environment variables.
  Missing credentials fail startup when SASL is enabled.
- The initial Kafka contract is standard Kafka protocol with plaintext or TLS
  transport and SASL PLAIN, SCRAM-SHA-256, or SCRAM-SHA-512 authentication.
- BORG does not create, alter, or delete Kafka topics and does not request broker
  auto-creation. Operators must provision `kafka.topic`.
- Invalid local settings, unsupported SASL mechanisms, missing credential
  environment variables, and missing, unreadable, or malformed TLS material fail
  startup. DNS, connection, broker authentication/authorization, and
  missing-topic failures are asynchronous and never affect readiness or proxy
  outcomes.
- Accepted records have no delivery timeout or retry limit. Unknown-topic retries
  are also unlimited, so records remain buffered until delivery, terminal broker
  rejection, or shutdown. Broker outages eventually fill the shared budget and
  cause newer events to be dropped.

## Event Contract

All events contain:

- `schema_version`: integer `1`;
- `event_id`: `<request_id>:<sequence>` for consumer deduplication;
- `event_type`;
- `request_id`: a BORG-generated random identifier;
- `sequence`: monotonically increasing per request, starting at zero;
- `timestamp`: UTC RFC3339Nano;
- `instance_id`: `HOSTNAME` when set, otherwise a process-random ID;
- `principal`, `model`, `stream`, and transformed `session_headers`.

Identity and session metadata use this bounded wire representation:

- `principal` and `model` contain at most 4,096 bytes and end on a UTF-8 rune
  boundary. Their sibling `<field>_original_bytes` and `<field>_truncated` fields
  are always present; `<field>_sha256` is present when truncation or invalid
  UTF-8 replacement changed the emitted value.
- Before applying the prefix limit, replace invalid UTF-8 sequences with the
  Unicode replacement rune. Hashes always cover the complete original bytes,
  not the repaired or truncated representation.
- Each `session_headers` map value is an ordered array of objects containing
  `value`, `value_mode`, `original_bytes`, and `truncated`. In `sha256` mode,
  `value` is the digest of the complete raw value and is never truncated. In
  `raw` mode, `value` follows the 4,096-byte UTF-8-safe limit and a separate
  `sha256` field is present when truncation or UTF-8 repair occurred.
- SHA-256 fields use lowercase hexadecimal prefixed with `sha256:`. Restricted
  credential headers are rejected before filtering and never reach this
  transformation or hashing path.

Version 1 event types are:

- `request.started`: method, path, content type, and total request bytes;
- `request.body_chunk`: base64 payload, byte offset, and byte count;
- `upstream.attempt`: attempt number and SHA-256 backend identifier, emitted
  immediately before `client.Do`;
- `upstream.result`: emitted exactly once when that attempt returns final headers
  or a setup error, with `result_kind`, optional HTTP `status`, and non-negative
  integer `attempt_duration_ms`;
- `response.started`: downstream status and content type;
- `response.body_chunk`: base64 payload, byte offset, and byte count;
- `request.completed`: normalized outcome, downstream status when known,
  non-negative integer `total_duration_ms`, request/response byte counts,
  captured byte counts, truncation flags, attempt count, and events dropped by
  the per-request recorder.

Payload chunks contain at most 32 KiB before base64 encoding. Capture stops at
the configured cumulative limit, marks the corresponding truncation flag, and
continues proxying normally. Consumers reconstruct exact captured bytes by
ordering events by `sequence`, decoding chunks, and applying their offsets.

Every serialized event value must be at most 262,144 bytes. Check this after
metadata bounding and before queue admission. If an event still exceeds the
ceiling because of aggregate metadata, drop it, advance its per-request sequence,
and include it in drop accounting without reserving queue capacity or changing
the proxied request.

For a matched request, downstream capture begins with the first explicit status
or implicit `200` write and includes both selected upstream responses and all
BORG-generated responses, including the current `404`, `500`, `502`, and `504`
paths. Record exactly the `n` bytes reported by each downstream `Write`,
including a partial write that also returns an error. Authentication failures,
oversized bodies, and malformed JSON remain outside capture because filtering
has not produced a valid request context.

Normalized upstream result kinds are `response`, `response_header_timeout`,
`transport_error`, and `client_cancelled`. Only `response` includes `status`, and
its duration ends when final headers arrive. A later selected-response body read
failure does not rewrite that result; it becomes completion `upstream_error`.

Normalized completion outcomes are `completed`, `client_cancelled`,
`client_write_error`, `unknown_model`, `response_header_timeout`,
`upstream_error`, and `internal_error`. When conditions overlap, use this exact
precedence: client write error, client cancellation, response-header timeout,
upstream error, internal error, unknown model, then completed. A fully delivered
selected upstream response is `completed` regardless of HTTP status, including
`500`, `502`, `503`, or `504`. A BORG-generated timeout is
`response_header_timeout`; exhausted transport attempts are `upstream_error`;
unexpected BORG failures are `internal_error`. Do not export raw internal error
strings.

Durations use Go's `time.Duration.Milliseconds()` conversion and clamp any
negative test-clock result to zero, so sub-millisecond durations serialize as
`0`.

Kafka ordering is guaranteed only within a partition. Multiple BORG replicas can
interleave concurrent requests for one session; request IDs and per-request
sequences remain authoritative.

## Helm Contract

Mirror `request_logging` under `config` and add chart-only Secret wiring:

```yaml
requestLoggingSecrets:
  kafkaCredentials:
    existingSecret: ""
    usernameKey: username
    passwordKey: password
  kafkaTLS:
    existingSecret: ""
    mountPath: /app/kafka-tls
terminationGracePeriodSeconds: 60
```

- Do not generate Kafka credentials or TLS material.
- When SASL is enabled, require `kafkaCredentials.existingSecret` and map its
  selected keys to the configured username/password environment names.
- When `kafkaTLS.existingSecret` is set, mount the whole Secret read-only at the
  configured path; runtime `ca_file`, `cert_file`, and `key_file` select files.
- Validate Kafka environment names with the existing portable environment-name
  rules and reject collisions with BORG, auth, TLS, and backend credential names.
- Include request-logging config in the existing ConfigMap checksum. External
  Secret contents remain the responsibility of a reloader or rollout restart.
- Keep the default chart render on `sink: noop` with no Kafka Secret references
  or TLS volume.
- Render `terminationGracePeriodSeconds` on the Pod. When the Kafka sink is
  enabled, require it to cover the fixed 30-second HTTP shutdown budget plus
  `shutdown_timeout_seconds`; the 60-second default covers the default 10-second
  logging flush with additional process-exit margin.

## Implementation Checkpoints

### 1. Configuration And Contracts

- [ ] Add runtime config types, defaults, validation, environment resolution,
  and input immutability tests.
- [ ] Define the versioned event types, JSON field names, normalized outcomes,
  request IDs, backend identifiers, and payload chunking helpers.
- [ ] Add golden JSON fixtures for every version 1 event type and exact enum
  tests so the producer and future consumers share a stable wire contract. Cover
  ordinary and truncated identity/session fields, duration units, and the largest
  legal body event.
- [ ] Implement compiled filter rules and session-header transformation.
- [ ] Add table tests for rule OR/AND behavior, RE2 failures, header
  canonicalization, duplicate and reserved headers, anonymous principal, UTF-8
  repair, 4,096-byte metadata limits, full-value digests, and capture limits.
- [ ] Test the 262,144-byte final-size check, sequence gaps and drop accounting for
  rejected oversized events, and assurance that serialized events or truncation
  metadata never contain inbound or backend credentials.

### 2. Recorder And Proxy Integration

- [ ] Add noop and recording implementations behind a narrow interface.
- [ ] Retain the principal returned by authentication and evaluate filters only
  after the model and stream mode are parsed.
- [ ] Emit request metadata/body events before forwarding matched traffic.
- [ ] Add a handler-owned response observer that records the first status,
  content type, and exactly the bytes accepted by downstream `Write` calls.
  Preserve `http.Flusher` behavior and expose `Unwrap` for standard response
  controller compatibility.
- [ ] Route BORG-generated unknown-model, upstream-exhaustion, timeout, and
  unexpected internal errors through the same observed writer. Keep auth,
  request-limit, and parse errors outside the recorder.
- [ ] Pass a separate narrow observer into the proxy for endpoint attempts,
  retryable statuses, and transport failures without changing failover or health
  accounting. The proxy must not own downstream body capture.
- [ ] Refactor response copying to return a normalized result for completion
  events, including partial downstream writes and upstream body read failures,
  while preserving current HTTP behavior. Apply the documented completion
  precedence when cancellation and a write or upstream error occur together.
- [ ] Emit `upstream.attempt` immediately before each `client.Do` and exactly one
  `upstream.result` when final headers or a setup error return. Keep selected-body
  read failures in completion accounting rather than rewriting attempt results.
- [ ] Make recorder enqueue strictly non-blocking and copy or encode payloads
  before the proxy buffer is reused.
- [ ] Emit completion on completed responses, unknown model, timeout, upstream
  exhaustion, unexpected internal error, cancellation, and client write failure
  whenever queue capacity permits.

### 3. Kafka Sink And Lifecycle

- [ ] Pin [`github.com/twmb/franz-go`](https://github.com/twmb/franz-go/releases/tag/v1.21.5)
  to `v1.21.5` and pin the `kfake` test module to the same upstream `1ba5fd2`
  commit. Add a small internal producer adapter so non-protocol tests can
  substitute a fake.
- [ ] Configure brokers, topic, client ID, TLS roots/client certificates, and
  SASL PLAIN/SCRAM from resolved config.
- [ ] Preserve franz-go idempotent producer behavior and do not add application
  retries that can duplicate records. Do not configure
  `kgo.RecordDeliveryTimeout` or `kgo.RecordRetries`; set
  `kgo.UnknownTopicRetries(-1)` and do not configure
  `kgo.AllowAutoTopicCreation`. Consumers still deduplicate by `event_id`.
- [ ] Drain one bounded application queue into the asynchronous producer using
  the computed Kafka key. Reserve shared record and `len(key)+len(value)` byte
  budget before enqueue and release it exactly once from delivery completion,
  terminal failure, or explicit shutdown drop, so the application channel and
  franz-go buffers cannot each consume the full limit.
- [ ] Configure franz-go record and byte limits no higher than the shared budget.
  Retain accepted older records and reject newer events at capacity. Verify
  accounting on successful delivery, asynchronous failure, enqueue drop,
  shutdown drop, and producer initialization failure.
- [ ] Load and validate TLS and SASL material before constructing the running
  sink. Fail startup for local configuration/material errors while leaving DNS,
  connection, broker auth/ACL, and missing-topic failures asynchronous.
- [ ] Keep BORG Ready when brokers are unavailable. Rate-limit diagnostic logs
  for queue drops and delivery failures, never include event payloads or keys in
  those diagnostics, and emit periodic aggregate counts.
- [ ] On `App.Close`, stop accepting events, drain the queue, flush Kafka for the
  configured timeout, then close without extending shutdown indefinitely. HTTP
  shutdown remains first so in-flight requests can enqueue their final events.
- [ ] Unit-test producer option construction for plaintext, TLS, PLAIN, and both
  SCRAM mechanisms, including invalid and missing credential combinations,
  unlimited record/unknown-topic retention, and disabled topic auto-creation.
- [ ] Use an in-process Kafka protocol broker for record keys, JSON payloads,
  ordering, missing topics, broker/authentication failures, retain-old/drop-new
  saturation, exactly-once budget release, and shutdown timeout. Keep real TLS
  and SASL handshakes in the host acceptance test.

### 4. Helm And Deployment

- [ ] Add values, JSON schema, ConfigMap rendering, Deployment env/volume wiring,
  and README examples.
- [ ] Add existing-Secret validation for SASL credentials and optional Kafka TLS
  material; never render secret values into the ConfigMap.
- [ ] Extend reserved environment collision checks and pod-template checksum
  tests.
- [ ] Add the 60-second termination grace default and validate the combined HTTP
  and Kafka shutdown budget when Kafka is enabled.
- [ ] Expand `scripts/validate-helm-chart.sh` for noop defaults, Kafka plaintext,
  TLS, SASL, missing Secret, invalid structural values, and checksum changes.
  Runtime Go tests, rather than Helm, own RE2 compilation checks.

### 5. End-To-End Validation And Documentation

- [ ] Add handler/proxy tests for principal/model/header filtering, capture-all,
  default-deny, normal responses, SSE reconstruction, retry attempts, unknown
  models, local errors, timeouts, partial writes, cancellations, truncation,
  exact result timing/kinds, completion precedence, shared byte/record accounting,
  and queue saturation.
- [ ] Prove noop and unmatched rules allocate no payload copies and leave proxy
  responses byte-for-byte unchanged.
- [ ] Add benchmarks for noop, metadata-only, and full stream capture paths.
- [ ] Document event schema, privacy boundaries, Kafka configuration, delivery
  guarantees, session partitioning, and consumer reconstruction.
- [ ] Run a host acceptance test against a real Kafka-compatible broker: deploy
  BORG with an existing credential/TLS Secret, send matched and unmatched normal
  and SSE requests, consume ordered events, reconstruct captured bytes, then
  stop the broker and prove BORG remains Ready and continues proxying.
- [ ] Implement that acceptance test as a dedicated host/raw-WSL script using
  explicit topic creation and the
  [official Apache Kafka 4.2.1 image](https://kafka.apache.org/community/downloads/),
  pinned as
  `apache/kafka:4.2.1@sha256:9916d60eca5d599550e2c320230808fda342124ba550bb4ac4ea8591803262a0`.
  Name it `scripts/validate-kafka-logging.sh`, exercise TLS and at least one SCRAM
  mechanism, and leave the remaining configured SASL mechanisms to unit tests.

## Quality Gates

```bash
go test ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
helm lint --strict charts/borg
bash scripts/validate-helm-chart.sh
bash -n scripts/validate-kind-go.sh
bash -n scripts/validate-kafka-logging.sh
git diff --check
```

Kafka or KinD acceptance runs must execute from the host/raw WSL environment,
not from the devcontainer.

## Out Of Scope

- Replacing the current bearer-token/auth-key scheme. The event contract uses a
  generic `principal` so a future auth provider can replace it.
- Guaranteed or transactional audit delivery, disk spooling, or failing model
  traffic when logging is unavailable.
- BORG-managed Kafka topic creation/administration and producer transactions.
- Logging rejected authentication attempts, oversized bodies, or invalid JSON
  bodies.
- Arbitrary request/response header capture, query-string capture, cookies, or
  authorization data.
- A Kafka consumer, analytics pipeline, UI, OpenTelemetry exporter, OPA work, or
  a Prometheus metrics endpoint.
- Kafka OAuth/OAUTHBEARER, AWS MSK IAM, or other provider-specific authentication
  plugins.

## Definition Of Done

- The default noop configuration is behaviorally equivalent to BORG `v0.2.0`.
- Explicit Kafka configuration exports only matching requests and allows exact
  reconstruction of captured normal and SSE response bytes.
- Kafka slowness, outage, queue saturation, and shutdown timeout never alter
  request status, failover, streaming, health accounting, or BORG readiness.
- Every emitted event respects the 262,144-byte ceiling; oversized identity and
  session metadata is represented by bounded prefixes and full-value digests,
  while aggregate oversize produces a counted sequence gap.
- Accepted records survive broker and missing-topic outages until delivery or
  shutdown; once the shared budget fills, newer events drop without displacing
  the retained backlog.
- No auth or backend credential material appears in configuration output,
  events, logs, or Helm ConfigMaps.
- Unit, race, lint, Helm, in-process Kafka, and host broker acceptance checks are
  green.
