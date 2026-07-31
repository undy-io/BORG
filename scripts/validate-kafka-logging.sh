#!/usr/bin/env bash
set -euo pipefail

kafka_image='apache/kafka:4.2.1@sha256:9916d60eca5d599550e2c320230808fda342124ba550bb4ac4ea8591803262a0'
kafka_port="${BORG_KAFKA_TEST_PORT:-19094}"
borg_port="${BORG_KAFKA_TEST_BORG_PORT:-18080}"
backend_port="${BORG_KAFKA_TEST_BACKEND_PORT:-18081}"
container="borg-kafka-logging-${USER:-user}-$$"
work_dir="$(mktemp -d)"
artifact_dir="${BORG_KAFKA_TEST_ARTIFACT_DIR:-}"
borg_pid=""
backend_pid=""

cleanup() {
	local status=$?
	set +e
	if [[ -n "$artifact_dir" ]]; then
	  mkdir -p "$artifact_dir"
	  for file in borg.log backend.log records.txt events.jsonl outage-records.txt outage-events.jsonl; do
	    if [[ -f "$work_dir/$file" ]]; then
	      cp "$work_dir/$file" "$artifact_dir/$file"
	    fi
	  done
	  docker logs "$container" > "$artifact_dir/kafka.log" 2>&1 || true
	fi
	if [[ -n "$borg_pid" ]]; then
    kill "$borg_pid" 2>/dev/null || true
    wait "$borg_pid" 2>/dev/null || true
  fi
  if [[ -n "$backend_pid" ]]; then
    kill "$backend_pid" 2>/dev/null || true
    wait "$backend_pid" 2>/dev/null || true
  fi
	docker rm -f "$container" >/dev/null 2>&1 || true
	rm -rf "$work_dir"
	exit "$status"
}
trap cleanup EXIT

for command in docker go curl jq openssl base64 cmp; do
  command -v "$command" >/dev/null || {
    echo "Required command is missing: $command" >&2
    exit 1
  }
done

wait_for_broker() {
	local timeout_seconds="$1"
	local deadline=$((SECONDS + timeout_seconds))

	while (( SECONDS < deadline )); do
		if [[ "$(docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null || true)" != "true" ]]; then
			echo 'Kafka broker exited during startup:' >&2
			docker logs "$container" >&2 || true
			return 1
		fi
		if docker exec "$container" /opt/kafka/bin/kafka-topics.sh \
			--bootstrap-server localhost:9092 --list >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done

	echo "Kafka broker did not become ready within ${timeout_seconds}s:" >&2
	docker logs "$container" >&2 || true
	return 1
}

consume_topic_snapshot() {
  local records_file="$1"
  local events_file="$2"
  docker exec "$container" /opt/kafka/bin/kafka-console-consumer.sh \
    --bootstrap-server localhost:9092 \
    --topic borg.request-events.v1 \
    --from-beginning \
    --timeout-ms 2000 \
    --formatter-property print.key=false > "$records_file" 2>/dev/null || true
  cp "$records_file" "$events_file"
  if ! jq -e -s 'all(.[]; type == "object")' "$events_file" >/dev/null 2>&1; then
    echo 'Kafka consumer returned a malformed event snapshot' >&2
    return 1
  fi
}

session_request_id() {
  local events_file="$1"
  local session="$2"
  jq -r -s --arg session "$session" \
    '[.[] | select(.event_type == "request.started" and .session_headers["x-session-id"][0].value == $session) | .request_id][0] // empty' \
    "$events_file"
}

session_completed() {
  local events_file="$1"
  local session="$2"
  local request_id
  request_id="$(session_request_id "$events_file" "$session")"
  [[ -n "$request_id" ]] && jq -e -s --arg id "$request_id" \
    'any(.[]; .event_type == "request.completed" and .request_id == $id)' \
    "$events_file" >/dev/null
}

wait_for_session_completions() {
  local records_file="$1"
  local events_file="$2"
  local timeout_seconds="$3"
  shift 3
  local deadline=$((SECONDS + timeout_seconds))
  local session
  local complete

  while (( SECONDS < deadline )); do
    consume_topic_snapshot "$records_file" "$events_file"
    complete=1
    for session in "$@"; do
      if ! session_completed "$events_file" "$session"; then
        complete=0
      fi
    done
    if [[ "$complete" -eq 1 ]]; then
      return 0
    fi
    sleep 1
  done

  consume_topic_snapshot "$records_file" "$events_file"
  echo "Timed out after ${timeout_seconds}s waiting for Kafka request completions" >&2
  for session in "$@"; do
    local request_id
    request_id="$(session_request_id "$events_file" "$session")"
    if [[ -z "$request_id" ]]; then
      echo "  ${session}: request.started missing" >&2
    elif session_completed "$events_file" "$session"; then
      echo "  ${session}: completed as ${request_id}" >&2
    else
      echo "  ${session}: request.completed missing for ${request_id}" >&2
    fi
  done
  return 1
}

assert_session_completed_once() {
  local events_file="$1"
  local session="$2"
  local request_id
  local count
  request_id="$(session_request_id "$events_file" "$session")"
  count="$(jq -s --arg id "$request_id" \
    '[.[] | select(.event_type == "request.completed" and .request_id == $id)] | length' \
    "$events_file")"
  if [[ -z "$request_id" || "$count" -ne 1 ]]; then
    echo "Expected one completed request for ${session}, got request_id=${request_id:-missing} completions=${count}" >&2
    exit 1
  fi
}

echo '==> Generating Kafka TLS material'
mkdir -p "$work_dir/tls"
chmod 0711 "$work_dir"
chmod 0755 "$work_dir/tls"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=BORG Kafka Test CA' \
  -keyout "$work_dir/tls/ca.key" \
  -out "$work_dir/tls/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes \
  -subj '/CN=localhost' \
  -keyout "$work_dir/tls/server.key" \
  -out "$work_dir/tls/server.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' > "$work_dir/tls/server.ext"
openssl x509 -req -days 1 \
  -in "$work_dir/tls/server.csr" \
  -CA "$work_dir/tls/ca.crt" \
  -CAkey "$work_dir/tls/ca.key" \
  -CAcreateserial \
  -extfile "$work_dir/tls/server.ext" \
  -out "$work_dir/tls/server.crt" >/dev/null 2>&1
openssl pkcs12 -export \
  -in "$work_dir/tls/server.crt" \
  -inkey "$work_dir/tls/server.key" \
  -certfile "$work_dir/tls/ca.crt" \
  -name kafka \
  -passout pass:changeit \
  -out "$work_dir/tls/server.p12" >/dev/null 2>&1
printf 'changeit\n' > "$work_dir/tls/keystore.creds"
printf 'changeit\n' > "$work_dir/tls/key.creds"
cat > "$work_dir/tls/kafka_server_jaas.conf" <<'EOF'
KafkaServer {
  org.apache.kafka.common.security.scram.ScramLoginModule required
  username="borg"
  password="borg-secret";
};
EOF
chmod 0644 \
	"$work_dir/tls/ca.crt" \
	"$work_dir/tls/server.p12" \
	"$work_dir/tls/keystore.creds" \
	"$work_dir/tls/key.creds" \
	"$work_dir/tls/kafka_server_jaas.conf"

echo '==> Starting pinned Kafka broker with TLS and SCRAM-SHA-256'
docker run -d --name "$container" --hostname kafka \
  -p "127.0.0.1:${kafka_port}:9094" \
  -v "$work_dir/tls:/etc/kafka/secrets:ro" \
  -e CLUSTER_ID='4L6g3nShT-eMCtK--X86sw' \
  -e KAFKA_NODE_ID=1 \
  -e KAFKA_PROCESS_ROLES=broker,controller \
  -e KAFKA_LISTENERS='INTERNAL://:9092,CONTROLLER://:9093,SASL_SSL://:9094' \
  -e "KAFKA_ADVERTISED_LISTENERS=INTERNAL://kafka:9092,SASL_SSL://localhost:${kafka_port}" \
  -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP='INTERNAL:PLAINTEXT,CONTROLLER:PLAINTEXT,SASL_SSL:SASL_SSL' \
  -e KAFKA_INTER_BROKER_LISTENER_NAME=INTERNAL \
  -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
  -e KAFKA_CONTROLLER_QUORUM_VOTERS='1@kafka:9093' \
  -e KAFKA_SASL_ENABLED_MECHANISMS='SCRAM-SHA-256' \
  -e KAFKA_OPTS='-Djava.security.auth.login.config=/etc/kafka/secrets/kafka_server_jaas.conf' \
  -e KAFKA_SSL_KEYSTORE_TYPE=PKCS12 \
  -e KAFKA_SSL_KEYSTORE_FILENAME=server.p12 \
  -e KAFKA_SSL_KEYSTORE_CREDENTIALS=keystore.creds \
  -e KAFKA_SSL_KEY_CREDENTIALS=key.creds \
  -e KAFKA_SSL_CLIENT_AUTH=none \
  -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
  -e KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS=0 \
  "$kafka_image" >/dev/null

wait_for_broker 60

docker exec "$container" /opt/kafka/bin/kafka-configs.sh \
  --bootstrap-server localhost:9092 \
  --alter \
  --add-config 'SCRAM-SHA-256=[iterations=8192,password=borg-secret]' \
  --entity-type users \
  --entity-name borg >/dev/null
docker exec "$container" /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 \
  --create \
  --topic borg.request-events.v1 \
  --partitions 3 \
  --replication-factor 1 >/dev/null

echo '==> Building and starting BORG plus dummy backend'
go build -o "$work_dir/borg" ./cmd/borg
go build -o "$work_dir/dummy-openai" ./dummy-openai
PORT="$backend_port" "$work_dir/dummy-openai" >"$work_dir/backend.log" 2>&1 &
backend_pid=$!

for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${backend_port}/v1/models" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:${backend_port}/v1/models" >/dev/null

cat > "$work_dir/config.yaml" <<EOF
borg:
  auth_key: EMPTY
  update_interval: -1
  instances:
    - endpoint: http://127.0.0.1:${backend_port}
      apikey: EMPTY
      models: [gpt-3.5-turbo]
  request_logging:
    sink: kafka
    queue_capacity: 1000
    queue_capacity_bytes: 67108864
    shutdown_timeout_seconds: 10
    capture:
      request_headers: true
      response_headers: true
      excluded_request_headers: [Authorization]
      excluded_response_headers: [Set-Cookie]
    session_headers:
      - name: X-Session-ID
        value_mode: raw
      - name: X-Capture
        value_mode: sha256
    partition_header: X-Session-ID
    filters:
      - headers:
          X-Capture: ['^yes$']
    kafka:
      brokers: [localhost:${kafka_port}]
      topic: borg.request-events.v1
      client_id: borg-acceptance
      tls:
        enabled: true
        server_name: localhost
        ca_file: ${work_dir}/tls/ca.crt
      sasl:
        mechanism: scram-sha-256
        username_from_env: BORG_KAFKA_USERNAME
        password_from_env: BORG_KAFKA_PASSWORD
EOF
BORG_KAFKA_USERNAME=borg \
BORG_KAFKA_PASSWORD=borg-secret \
PORT="$borg_port" \
  "$work_dir/borg" --config "$work_dir/config.yaml" >"$work_dir/borg.log" 2>&1 &
borg_pid=$!

for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${borg_port}/" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:${borg_port}/" >/dev/null

echo '==> Sending unmatched, matched, and streaming requests'
curl -fsS -X POST "http://127.0.0.1:${borg_port}/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","marker":"unmatched-marker"}' >/dev/null

normal_request='{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"normal"}]}'
printf '%s' "$normal_request" > "$work_dir/normal-request.json"
curl -fsS -X POST "http://127.0.0.1:${borg_port}/v1/chat/completions" \
	-H 'Content-Type: application/json' \
	-H 'Authorization: Bearer inbound-sensitive' \
	-H 'X-API-Key: privileged-api-key' \
	-H 'X-Capture: yes' \
  -H 'X-Session-ID: session-normal' \
  -d "$normal_request" > "$work_dir/normal-response.json"

stream_request='{"model":"gpt-3.5-turbo","stream":true}'
printf '%s' "$stream_request" > "$work_dir/stream-request.json"
curl -fsS -N -X POST "http://127.0.0.1:${borg_port}/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -H 'X-Capture: yes' \
  -H 'X-Session-ID: session-stream' \
  -d "$stream_request" > "$work_dir/stream-response.txt"

wait_for_session_completions \
  "$work_dir/records.txt" "$work_dir/events.jsonl" 60 \
  session-normal session-stream

if grep -q 'unmatched-marker' "$work_dir/events.jsonl"; then
  echo 'Unmatched request was exported' >&2
  exit 1
fi
jq -s -e 'all(.[]; .schema_version == 1 and (.event_id | length > 0))' \
  "$work_dir/events.jsonl" >/dev/null
assert_session_completed_once "$work_dir/events.jsonl" session-normal
assert_session_completed_once "$work_dir/events.jsonl" session-stream
if ! jq -e 'select(.event_type == "request.header" and .header_name == "x-api-key" and .value == "privileged-api-key")' \
	"$work_dir/events.jsonl" >/dev/null; then
	echo 'Permitted sensitive request header was not exported' >&2
	exit 1
fi
if jq -e 'select(.event_type == "request.header" and .header_name == "authorization")' \
	"$work_dir/events.jsonl" >/dev/null; then
	echo 'Excluded Authorization request header was exported' >&2
	exit 1
fi

reconstruct() {
  local session="$1"
  local event_type="$2"
  local output="$3"
  local request_id
  request_id="$(session_request_id "$work_dir/events.jsonl" "$session")"
  if [[ -z "$request_id" ]]; then
    echo "No request events found for $session" >&2
    exit 1
  fi
  jq -r -s --arg id "$request_id" --arg type "$event_type" \
    '[.[] | select(.request_id == $id and .event_type == $type)] | sort_by(.sequence) | .[].payload' \
    "$work_dir/events.jsonl" | base64 --decode > "$output"
}

reconstruct session-normal request.body_chunk "$work_dir/reconstructed-normal-request.json"
reconstruct session-normal response.body_chunk "$work_dir/reconstructed-normal-response.json"
reconstruct session-stream request.body_chunk "$work_dir/reconstructed-stream-request.json"
reconstruct session-stream response.body_chunk "$work_dir/reconstructed-stream-response.txt"
cmp "$work_dir/normal-request.json" "$work_dir/reconstructed-normal-request.json"
cmp "$work_dir/normal-response.json" "$work_dir/reconstructed-normal-response.json"
cmp "$work_dir/stream-request.json" "$work_dir/reconstructed-stream-request.json"
cmp "$work_dir/stream-response.txt" "$work_dir/reconstructed-stream-response.txt"

echo '==> Confirming broker outage does not affect readiness or proxying'
docker stop "$container" >/dev/null
curl -fsS "http://127.0.0.1:${borg_port}/" >/dev/null
curl -fsS -X POST "http://127.0.0.1:${borg_port}/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -H 'X-Capture: yes' \
	-H 'X-Session-ID: session-broker-down' \
	-d '{"model":"gpt-3.5-turbo","marker":"broker-down"}' >/dev/null

echo '==> Restarting broker and confirming retained outage events are delivered'
docker start "$container" >/dev/null
wait_for_broker 60

wait_for_session_completions \
  "$work_dir/outage-records.txt" "$work_dir/outage-events.jsonl" 60 \
  session-broker-down
assert_session_completed_once "$work_dir/outage-events.jsonl" session-broker-down

echo '==> Kafka request logging validation passed'
