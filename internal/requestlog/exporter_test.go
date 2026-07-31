package requestlog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type controlledSink struct {
	mu        sync.Mutex
	records   []Record
	callbacks []func(error)
	closed    bool
}

type immediateSink struct{}

func (immediateSink) Produce(_ Record, callback func(error)) { callback(nil) }
func (immediateSink) Flush(context.Context) error            { return nil }
func (immediateSink) Close()                                 {}

func (s *controlledSink) Produce(record Record, callback func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	s.callbacks = append(s.callbacks, callback)
}

func (s *controlledSink) Flush(context.Context) error { return nil }
func (s *controlledSink) Close() {
	s.mu.Lock()
	s.closed = true
	callbacks := append([]func(error){}, s.callbacks...)
	s.callbacks = nil
	s.mu.Unlock()
	for _, callback := range callbacks {
		callback(ErrExporterClosed)
	}
}

func (s *controlledSink) deliver(index int, err error) {
	s.mu.Lock()
	callback := s.callbacks[index]
	s.callbacks[index] = func(error) {}
	s.mu.Unlock()
	callback(err)
}

func TestExporterRetainsOldAndDropsNew(t *testing.T) {
	sink := &controlledSink{}
	exporter, err := NewExporter(2, 8, sink)
	if err != nil {
		t.Fatal(err)
	}
	first := Record{Key: []byte("k"), Value: []byte("one")}
	second := Record{Key: []byte("k"), Value: []byte("two")}
	if !exporter.TryExport(first) || !exporter.TryExport(second) {
		t.Fatal("expected first two records to be accepted")
	}
	if exporter.TryExport(Record{Key: []byte("k"), Value: []byte("new")}) {
		t.Fatal("expected newer record to be rejected at capacity")
	}
	waitForSinkRecords(t, sink, 2)
	if stats := exporter.Stats(); stats.ReservedRecords != 2 || stats.ReservedBytes != 8 {
		t.Fatalf("unexpected reservations: %#v", stats)
	}
	sink.deliver(0, nil)
	if stats := exporter.Stats(); stats.ReservedRecords != 1 || stats.ReservedBytes != 4 {
		t.Fatalf("delivery did not release exactly one reservation: %#v", stats)
	}
	sink.deliver(0, errors.New("duplicate callback"))
	if stats := exporter.Stats(); stats.ReservedRecords != 1 || stats.ReservedBytes != 4 {
		t.Fatalf("duplicate callback released reservation twice: %#v", stats)
	}
	sink.deliver(1, errors.New("terminal"))
	if stats := exporter.Stats(); stats != (ExporterStats{}) {
		t.Fatalf("terminal failure did not release reservation: %#v", stats)
	}
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exporter.TryExport(first) {
		t.Fatal("closed exporter accepted a record")
	}
}

func TestExporterByteCapacityIncludesKey(t *testing.T) {
	sink := &controlledSink{}
	exporter, err := NewExporter(10, 5, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !exporter.TryExport(Record{Key: []byte("kk"), Value: []byte("vvv")}) {
		t.Fatal("exact byte budget should be accepted")
	}
	if exporter.TryExport(Record{Value: []byte("x")}) {
		t.Fatal("record above reserved byte budget should be rejected")
	}
	waitForSinkRecords(t, sink, 1)
	sink.deliver(0, nil)
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExporterConcurrentAdmissionReleasesAllReservations(t *testing.T) {
	exporter, err := NewExporter(32, 1024, immediateSink{})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 100 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				_ = exporter.TryExport(Record{Key: []byte("key"), Value: []byte("value")})
			}
		}()
	}
	workers.Wait()
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := exporter.Stats(); stats != (ExporterStats{}) {
		t.Fatalf("concurrent delivery leaked reservations: %#v", stats)
	}
}

func TestExporterTracksLocalEncodingDrops(t *testing.T) {
	exporter, err := NewExporter(1, 1024, immediateSink{})
	if err != nil {
		t.Fatal(err)
	}
	exporter.RecordLocalDrop(LocalDropEventTooLarge)
	exporter.RecordLocalDrop(LocalDropEventTooLarge)
	exporter.RecordLocalDrop(LocalDropEventEncodingFailed)

	diagnostics := exporter.Diagnostics()
	if diagnostics.EventTooLarge != 2 || diagnostics.EventEncodingFailed != 1 {
		t.Fatalf("unexpected local drop diagnostics: %#v", diagnostics)
	}
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func waitForSinkRecords(t *testing.T, sink *controlledSink, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		sink.mu.Lock()
		got := len(sink.records)
		sink.mu.Unlock()
		if got >= count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d sink records, want %d", got, count)
		}
		time.Sleep(time.Millisecond)
	}
}
