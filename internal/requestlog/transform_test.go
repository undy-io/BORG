package requestlog

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBoundValue(t *testing.T) {
	raw := strings.Repeat("a", MaxMetadataValueBytes-1) + "é"
	got := BoundValue(raw)
	if !got.Truncated || len(got.Value) > MaxMetadataValueBytes || !utf8.ValidString(got.Value) {
		t.Fatalf("value was not safely truncated: %#v", got)
	}
	if got.OriginalBytes != len(raw) || got.SHA256 != Digest([]byte(raw)) {
		t.Fatalf("truncation metadata does not describe the original: %#v", got)
	}

	invalid := string([]byte{'a', 0xff, 'b'})
	got = BoundValue(invalid)
	if got.Truncated || got.Value != "a�b" || got.OriginalBytes != 3 || got.SHA256 != Digest([]byte(invalid)) {
		t.Fatalf("invalid UTF-8 was not repaired correctly: %#v", got)
	}
}

func TestTransformSessionHeaders(t *testing.T) {
	headers := make(http.Header)
	headers.Add("X-Raw", strings.Repeat("x", MaxMetadataValueBytes+1))
	headers.Add("X-Hash", "one")
	headers.Add("X-Hash", "two")
	got, raw := TransformSessionHeaders(headers, []SessionHeader{
		{Name: "x-raw", ValueMode: ValueModeRaw},
		{Name: "x-hash", ValueMode: ValueModeSHA256},
	})
	if len(got["x-hash"]) != 2 || got["x-hash"][0].Value != Digest([]byte("one")) || got["x-hash"][0].SHA256 != "" {
		t.Fatalf("unexpected hashed values: %#v", got["x-hash"])
	}
	if !got["x-raw"][0].Truncated || got["x-raw"][0].SHA256 == "" {
		t.Fatalf("raw truncation metadata missing: %#v", got["x-raw"])
	}
	if raw["x-hash"][1] != "two" {
		t.Fatalf("raw values were not retained for filtering/partitioning: %#v", raw)
	}
}

func TestCaptureHeadersOrdersBoundsAndExcludesValues(t *testing.T) {
	long := strings.Repeat("x", MaxMetadataValueBytes+1)
	headers := http.Header{
		"x-mixed":       {"third"},
		"X-Mixed":       {"first", ""},
		"X-Long":        {long},
		"Authorization": {"Bearer privileged"},
	}
	got := CaptureHeaders(headers, map[string]struct{}{"authorization": {}})
	if len(got) != 4 {
		t.Fatalf("captured %d values, want 4: %#v", len(got), got)
	}
	if got[0].Name != "x-long" || !got[0].Truncated || got[0].SHA256 != Digest([]byte(long)) {
		t.Fatalf("long value was not bounded: %#v", got[0])
	}
	for index, want := range []string{"first", "", "third"} {
		value := got[index+1]
		if value.Name != "x-mixed" || value.ValueIndex != index || value.Value != want {
			t.Fatalf("mixed-case value %d = %#v, want %q", index, value, want)
		}
	}
}

func TestRequestHeaderViewUsesDedicatedHost(t *testing.T) {
	headers := http.Header{
		"Host":    {"synthetic.invalid"},
		"X-First": {"one"},
	}
	view := requestHeaderView(headers, "borg.internal:8443")
	transformed, raw := transformSessionHeaders(view, []SessionHeader{{Name: "host", ValueMode: ValueModeRaw}})
	if got := transformed["host"]; len(got) != 1 || got[0].Value != "borg.internal:8443" {
		t.Fatalf("session Host = %#v", got)
	}
	if got := raw["host"]; len(got) != 1 || got[0] != "borg.internal:8443" {
		t.Fatalf("raw Host = %#v", got)
	}

	captured := captureHeaders(view, nil)
	if len(captured) != 2 || captured[0].Name != "host" || captured[0].Value != "borg.internal:8443" ||
		captured[1].Name != "x-first" || captured[1].Value != "one" {
		t.Fatalf("captured request headers = %#v", captured)
	}
	if got := captureHeaders(view, map[string]struct{}{"host": {}}); len(got) != 1 || got[0].Name != "x-first" {
		t.Fatalf("Host exclusion produced %#v", got)
	}
	if got := captureHeaders(requestHeaderView(headers, ""), nil); len(got) != 1 || got[0].Name != "x-first" {
		t.Fatalf("empty dedicated Host captured a synthetic value: %#v", got)
	}
}

func TestRequestHostPartitionKey(t *testing.T) {
	declared := map[string][]string{}
	firstValues := partitionValues(requestHeaderView(nil, "borg.internal"), "host", declared)
	secondValues := partitionValues(requestHeaderView(http.Header{"Host": {"synthetic.invalid"}}, "borg.internal"), "host", declared)
	differentValues := partitionValues(requestHeaderView(nil, "other.internal"), "host", declared)
	first := PartitionKey("host", firstValues, "request-one")
	second := PartitionKey("host", secondValues, "request-two")
	different := PartitionKey("host", differentValues, "request-one")
	if !bytes.Equal(first, second) || bytes.Equal(first, different) {
		t.Fatalf("Host partition keys were not stable and distinct: %q %q %q", first, second, different)
	}
	if got := PartitionKey("host", partitionValues(requestHeaderView(nil, ""), "host", declared), "request-id"); string(got) != "request-id" {
		t.Fatalf("empty Host partition key = %q, want request ID fallback", got)
	}
}

func TestPartitionKey(t *testing.T) {
	if got := PartitionKey("", nil, "request-id"); string(got) != "request-id" {
		t.Fatalf("unexpected fallback key %q", got)
	}
	raw := map[string][]string{"x-session": {"ab", "c"}}
	first := PartitionKey("x-session", raw, "request-id")
	second := PartitionKey("x-session", raw, "other")
	ambiguousAlternative := PartitionKey("x-session", map[string][]string{"x-session": {"a", "bc"}}, "request-id")
	if !bytes.Equal(first, second) || bytes.Equal(first, ambiguousAlternative) || !bytes.HasPrefix(first, []byte("sha256:")) {
		t.Fatalf("partition encoding is not stable and unambiguous: %q %q", first, ambiguousAlternative)
	}
}

func TestDurationMilliseconds(t *testing.T) {
	if got := DurationMilliseconds(-time.Second); got != 0 {
		t.Fatalf("negative duration = %d", got)
	}
	if got := DurationMilliseconds(1500 * time.Microsecond); got != 1 {
		t.Fatalf("duration = %d", got)
	}
}

func TestBodyChunks(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), BodyChunkBytes*2+5)
	chunks, truncated := BodyChunks(payload, int64(BodyChunkBytes+3))
	if !truncated || len(chunks) != 2 || len(chunks[0]) != BodyChunkBytes || len(chunks[1]) != 3 {
		t.Fatalf("unexpected chunks: lengths=%v truncated=%v", []int{len(chunks[0]), len(chunks[1])}, truncated)
	}
	chunks[0][0] = 'y'
	if payload[0] != 'x' {
		t.Fatal("chunks must not alias the caller payload")
	}

	unlimited, truncated := BodyChunks(payload, 0)
	if truncated || len(unlimited) != 3 {
		t.Fatalf("unlimited capture was truncated: %d %v", len(unlimited), truncated)
	}
}

func TestIdentifiers(t *testing.T) {
	one := NewRequestID()
	two := NewRequestID()
	if one == "" || one == two {
		t.Fatalf("request IDs are not distinct: %q %q", one, two)
	}
	if got := BackendIdentifier("http://backend"); got != Digest([]byte("http://backend")) {
		t.Fatalf("unexpected backend identifier %q", got)
	}
}
