package requestlog

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func NewRequestID() string {
	return rand.Text()
}

func BackendIdentifier(endpoint string) string {
	return Digest([]byte(endpoint))
}

func BodyChunks(payload []byte, limit int64) ([][]byte, bool) {
	captureBytes := int64(len(payload))
	truncated := false
	if limit > 0 && captureBytes > limit {
		captureBytes = limit
		truncated = true
	}
	chunks := make([][]byte, 0, (captureBytes+BodyChunkBytes-1)/BodyChunkBytes)
	for offset := int64(0); offset < captureBytes; offset += BodyChunkBytes {
		end := min(offset+BodyChunkBytes, captureBytes)
		chunks = append(chunks, append([]byte(nil), payload[offset:end]...))
	}
	return chunks, truncated
}

type BoundedValue struct {
	Value         string
	OriginalBytes int
	Truncated     bool
	SHA256        string
}

type CapturedHeaderValue struct {
	Name       string
	ValueIndex int
	BoundedValue
}

type headerView struct {
	headers       http.Header
	host          string
	dedicatedHost bool
}

func plainHeaderView(headers http.Header) headerView {
	return headerView{headers: headers}
}

func requestHeaderView(headers http.Header, host string) headerView {
	return headerView{headers: headers, host: host, dedicatedHost: true}
}

func (h headerView) Values(name string) []string {
	if h.dedicatedHost && strings.EqualFold(name, "host") {
		if h.host == "" {
			return nil
		}
		return []string{h.host}
	}
	return h.headers.Values(name)
}

func (h headerView) capturedValues(sourceName string) []string {
	if h.dedicatedHost && strings.EqualFold(sourceName, "host") {
		return []string{h.host}
	}
	return h.headers[sourceName]
}

func BoundValue(raw string) BoundedValue {
	value := raw
	changed := false
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, string(utf8.RuneError))
		changed = true
	}
	truncated := len(value) > MaxMetadataValueBytes
	if truncated {
		value = truncateUTF8(value, MaxMetadataValueBytes)
		changed = true
	}
	bounded := BoundedValue{
		Value:         value,
		OriginalBytes: len(raw),
		Truncated:     truncated,
	}
	if changed {
		bounded.SHA256 = Digest([]byte(raw))
	}
	return bounded
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func TransformSessionHeaders(headers http.Header, declared []SessionHeader) (map[string][]SessionHeaderValue, map[string][]string) {
	return transformSessionHeaders(plainHeaderView(headers), declared)
}

func transformSessionHeaders(headers headerView, declared []SessionHeader) (map[string][]SessionHeaderValue, map[string][]string) {
	transformed := make(map[string][]SessionHeaderValue, len(declared))
	raw := make(map[string][]string, len(declared))
	for _, header := range declared {
		values := headers.Values(header.Name)
		raw[header.Name] = append([]string(nil), values...)
		wireValues := make([]SessionHeaderValue, 0, len(values))
		for _, value := range values {
			wire := SessionHeaderValue{
				ValueMode:     header.ValueMode,
				OriginalBytes: len(value),
			}
			if header.ValueMode == ValueModeSHA256 {
				wire.Value = Digest([]byte(value))
			} else {
				bounded := BoundValue(value)
				wire.Value = bounded.Value
				wire.Truncated = bounded.Truncated
				wire.SHA256 = bounded.SHA256
			}
			wireValues = append(wireValues, wire)
		}
		transformed[header.Name] = wireValues
	}
	return transformed, raw
}

func CaptureHeaders(headers http.Header, excluded map[string]struct{}) []CapturedHeaderValue {
	return captureHeaders(plainHeaderView(headers), excluded)
}

func captureHeaders(headers headerView, excluded map[string]struct{}) []CapturedHeaderValue {
	names := make([]string, 0, len(headers.headers)+1)
	for name := range headers.headers {
		if headers.dedicatedHost && strings.EqualFold(name, "host") {
			continue
		}
		names = append(names, name)
	}
	if headers.dedicatedHost && headers.host != "" {
		names = append(names, "Host")
	}
	sort.Slice(names, func(i, j int) bool {
		left := strings.ToLower(names[i])
		right := strings.ToLower(names[j])
		if left == right {
			return names[i] < names[j]
		}
		return left < right
	})

	indexes := make(map[string]int, len(names))
	values := make([]CapturedHeaderValue, 0, len(names))
	for _, sourceName := range names {
		name := strings.ToLower(sourceName)
		if _, skip := excluded[name]; skip {
			continue
		}
		for _, value := range headers.capturedValues(sourceName) {
			values = append(values, CapturedHeaderValue{
				Name:         name,
				ValueIndex:   indexes[name],
				BoundedValue: BoundValue(value),
			})
			indexes[name]++
		}
	}
	return values
}

func PartitionKey(partitionHeader string, rawHeaders map[string][]string, requestID string) []byte {
	if partitionHeader == "" || len(rawHeaders[partitionHeader]) == 0 {
		return []byte(requestID)
	}
	hasher := sha256.New()
	var length [8]byte
	values := rawHeaders[partitionHeader]
	binary.BigEndian.PutUint64(length[:], uint64(len(values)))
	_, _ = hasher.Write(length[:])
	for _, value := range values {
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(value))
	}
	return []byte("sha256:" + hex.EncodeToString(hasher.Sum(nil)))
}

func DurationMilliseconds(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}
