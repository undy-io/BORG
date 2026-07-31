package requestlog

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrEventTooLarge = errors.New("request logging event exceeds serialized size limit")

func Encode(event Event) ([]byte, error) {
	if event == nil {
		return nil, errors.New("request logging event is nil")
	}
	common := event.eventCommon()
	if common.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("request logging schema_version must be %d", SchemaVersion)
	}
	if common.EventType != event.eventType() {
		return nil, fmt.Errorf("request logging event_type must be %q", event.eventType())
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode request logging event: %w", err)
	}
	if len(encoded) > MaxEventValueBytes {
		return nil, fmt.Errorf("%w: got %d bytes, maximum is %d", ErrEventTooLarge, len(encoded), MaxEventValueBytes)
	}
	return encoded, nil
}
