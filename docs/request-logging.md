# Request Event Logging

## Purpose

BORG can export ordered, versioned request events to Kafka for internal audit,
usage analysis, and reconstruction of selected request/response streams. Logging
is disabled by default with `sink: noop`. Export failures, unavailable brokers,
missing topics, queue saturation, encoding failures, and shutdown drops do not
change proxy responses or readiness.

## Filtering And Sensitive Data

Only authenticated, size-valid, JSON-valid POST requests are eligible. Rules are
ORed; principal, model, and header constraints inside a rule are ANDed; patterns
inside one constraint are ORed. Patterns are Go RE2 expressions compiled during
startup. No rules means capture nothing, while one empty rule (`- {}`) captures
all eligible requests.

The `principal` is the identity recovered from BORG's current bearer token, or
`ANONYMOUS` when inbound authentication is disabled. Kafka is a privileged log:
captured headers and opaque bodies may contain bearer tokens, backend
credentials, cookies, personal data, or proprietary model input and output.
Operators must apply appropriate Kafka transport encryption, topic ACLs,
retention, and consumer-access controls.

Session header values default to SHA-256 capture. Raw capture is explicit. Raw
principal, model, and session values are repaired to valid UTF-8 and limited to
a 4,096-byte rune-safe prefix. Truncated or repaired values include the original
byte count and a SHA-256 digest of the complete original value. Hash-only session
values emit only the complete-value digest.

Filtering and partitioning may name any valid request header and always evaluate
complete raw values. They do not require a `session_headers` declaration and are
not affected by generic header-capture exclusions. When `partition_header` is
present, all of its ordered raw values are encoded with
fixed big-endian counts and lengths, then hashed for the Kafka key. Otherwise,
the generated request ID is the key.

Generic request and response header capture is independently opt-in. Exclusion
lists use case-insensitive exact header names and default empty; BORG has no
hard-coded sensitive-header list. Explicit `session_headers` remain separate
metadata and are emitted even when the same name is excluded from generic header
events.

## Event Contract

Version 1 uses flat JSON objects. Every event includes:

- `schema_version`, currently `1`;
- `event_id`, formatted as `<request_id>:<sequence>`;
- `event_type`, `request_id`, monotonic `sequence`, and UTC `timestamp`;
- `instance_id`, `principal`, `model`, `stream`, and `session_headers`;
- identity truncation, original-byte-count, and full-value hash metadata.

Event types are `request.started`, `request.header`, `request.body_chunk`,
`upstream.attempt`, `upstream.result`, `response.started`, `response.header`,
`response.body_chunk`, and `request.completed`.

`request.started.path` contains the escaped URL path only. Query data is
forwarded upstream but is intentionally excluded from request events.

Body payloads are base64 JSON strings backed by chunks of at most 32 KiB. To
reconstruct captured bytes, group by `request_id`, order by `sequence`, decode
body chunk payloads, and apply `byte_offset`. Capture limits can produce a valid
prefix; completion events carry captured byte counts and truncation flags.

`request.header` represents inbound client headers before BORG normalizes or
rewrites the upstream request. `response.header` represents downstream-visible
headers at final response commitment. BORG-generated upstream headers, automatic
HTTP framing, and trailers are not captured. Header names are lowercase and each
ordered value is a separate event with `value_index`. Values use the same
4,096-byte UTF-8-safe prefix and complete-value hash metadata as raw session
values.

`upstream.result.result_kind` is one of `response`,
`response_header_timeout`, `transport_error`, or `client_cancelled`. Only a
response includes HTTP status. Attempt and total durations are non-negative
integer milliseconds.

Completion outcomes are `completed`, `client_write_error`, `client_cancelled`,
`response_header_timeout`, `upstream_error`, `internal_error`, and
`unknown_model`. A fully delivered upstream response is completed regardless of
HTTP status. `events_dropped` counts local failures before queue admission that
precede the completion event: encoding failure, final-size rejection, and queue
saturation.

Sequence numbers are assigned before queue admission and Kafka delivery.
Consumer-visible gaps can therefore represent either those locally counted
drops or post-admission loss from Kafka delivery failure, process termination,
or shutdown. Post-admission loss is reported only through exporter diagnostics
and is not included in `events_dropped`. If `request.completed` itself is not
delivered, its final per-request accounting is also unavailable to consumers.

Each serialized event value is limited to 262,144 bytes. An oversized event is
dropped locally, advances the sequence, and does not reserve queue capacity.

## Kafka Delivery

BORG uses franz-go `v1.21.5` with idempotent production, unlimited record
retention/retries, unlimited unknown-topic retries, and broker topic
auto-creation disabled. Operators must create the configured topic.

The shared in-memory budget defaults to 100,000 accepted records and 4 GiB of
encoded Kafka key plus value bytes. Admission is non-blocking. At capacity,
older accepted records remain and newer events drop. Reservations remain until
delivery callback, terminal failure, or shutdown drop. The budget excludes Go,
TLS, compression, and Kafka client bookkeeping overhead, so Kafka-enabled pods
need an appropriate memory limit.

Kafka records are sensitive operational data. Topic administrators own broker
encryption, ACLs, retention, deletion, and consumer authorization. BORG keeps
configured Secret values out of rendered ConfigMaps and omits record keys and
payloads from its own diagnostics; those operational safeguards do not sanitize
the events selected for capture.

There is no disk spool, transaction, replay database, or readiness dependency.
On shutdown, BORG stops admission, drains the application channel, attempts one
bounded producer flush, aborts remaining buffered records, and closes the
producer. Delivery failures and shutdown drops remain visible through aggregate
exporter diagnostics rather than individual request completion events.

## Configuration

```yaml
borg:
  request_logging:
    sink: kafka
    queue_capacity: 100000
    queue_capacity_bytes: 4294967296
    shutdown_timeout_seconds: 10
    capture:
      request_body: true
      response_body: true
      request_headers: true
      response_headers: true
      excluded_request_headers:
        - Authorization
        - Proxy-Authorization
        - Cookie
      excluded_response_headers:
        - Set-Cookie
      max_request_body_bytes: 524288
      max_response_body_bytes: 16777216
    session_headers:
      - name: X-Session-ID
        value_mode: sha256
    partition_header: X-Session-ID
    filters:
      - principals: ["^team-a$"]
        models: ["^Qwen/"]
        headers:
          X-Session-ID: [".+"]
    kafka:
      brokers: ["kafka.messaging.svc:9093"]
      topic: borg.request-events.v1
      client_id: borg
      tls:
        enabled: true
        server_name: kafka.messaging.svc
        ca_file: /app/kafka-tls/ca.crt
        cert_file: ""
        key_file: ""
      sasl:
        mechanism: scram-sha-256
        username_from_env: BORG_KAFKA_USERNAME
        password_from_env: BORG_KAFKA_PASSWORD
```

For Helm, set `requestLoggingSecrets.kafkaCredentials.existingSecret` for SASL.
The Secret keys default to `username` and `password`. Set
`requestLoggingSecrets.kafkaTLS.existingSecret` when Kafka TLS file paths are
configured; those paths must be below the read-only
`requestLoggingSecrets.kafkaTLS.mountPath`.

External Secret contents are outside Helm checksum state. Use the chart's
`podAnnotations` with your Secret reloader or restart the Deployment after
credential rotation.

## Validation

Unit and kfake protocol tests run with `go test ./...`. The real broker harness
starts BORG directly and validates real TLS/SASL delivery, reconstruction,
broker-outage independence, and retained delivery after recovery. It must run
from host/raw WSL because Docker is unavailable in the devcontainer:

```bash
scripts/validate-kafka-logging.sh
```

Strict Helm chart validation separately owns Kubernetes existing-Secret
rendering, environment wiring, TLS mounts, and credential sanitization.
The logging-enabled KinD path validates the chart, runtime, in-cluster broker,
header policy, and body reconstruction together:

```bash
scripts/validate-kind-go.sh --create-cluster --delete-cluster --with-kafka-logging
```

GitHub-hosted Docker runners execute both acceptance paths through
`.github/workflows/integration.yml`.
