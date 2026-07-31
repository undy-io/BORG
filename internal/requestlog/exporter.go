package requestlog

import (
	"context"
	"errors"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

var ErrExporterClosed = errors.New("request logging exporter is closed")

type Record struct {
	Key   []byte
	Value []byte
}

type RecordSink interface {
	Produce(Record, func(error))
	Flush(context.Context) error
	Close()
}

type EventExporter interface {
	TryExport(Record) bool
}

type LocalDropReason string

const (
	LocalDropEventTooLarge       LocalDropReason = "event_too_large"
	LocalDropEventEncodingFailed LocalDropReason = "event_encoding_failed"
)

type LocalDropReporter interface {
	RecordLocalDrop(LocalDropReason)
}

type Exporter struct {
	sink RecordSink

	mu             sync.Mutex
	accepting      bool
	reservedCount  int
	reservedBytes  int64
	capacityCount  int
	capacityBytes  int64
	records        chan reservedRecord
	workerFinished chan struct{}
	closeOnce      sync.Once

	accepted              atomic.Uint64
	rejected              atomic.Uint64
	delivered             atomic.Uint64
	failed                atomic.Uint64
	eventTooLarge         atomic.Uint64
	eventEncodingFailed   atomic.Uint64
	diagnosticMu          sync.Mutex
	lastFailureDiagnostic time.Time
}

type reservedRecord struct {
	record  Record
	release func()
}

type ExporterStats struct {
	ReservedRecords int
	ReservedBytes   int64
}

type ExporterDiagnostics struct {
	Accepted            uint64
	Rejected            uint64
	Delivered           uint64
	Failed              uint64
	EventTooLarge       uint64
	EventEncodingFailed uint64
}

func NewExporter(capacityCount int, capacityBytes int64, sink RecordSink) (*Exporter, error) {
	if capacityCount <= 0 || capacityBytes <= 0 {
		return nil, errors.New("request logging exporter capacities must be positive")
	}
	if sink == nil {
		return nil, errors.New("request logging exporter sink is required")
	}
	exporter := &Exporter{
		sink:           sink,
		accepting:      true,
		capacityCount:  capacityCount,
		capacityBytes:  capacityBytes,
		records:        make(chan reservedRecord, capacityCount),
		workerFinished: make(chan struct{}),
	}
	go exporter.run()
	return exporter, nil
}

func (e *Exporter) TryExport(record Record) bool {
	if e == nil {
		return false
	}
	keySize := int64(len(record.Key))
	valueSize := int64(len(record.Value))
	if keySize > math.MaxInt64-valueSize {
		e.rejected.Add(1)
		return false
	}
	size := keySize + valueSize
	if size > e.capacityBytes {
		e.rejected.Add(1)
		return false
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting || e.reservedCount >= e.capacityCount || e.reservedBytes > e.capacityBytes-size {
		e.rejected.Add(1)
		return false
	}

	e.reservedCount++
	e.reservedBytes += size
	var once sync.Once
	release := func() {
		once.Do(func() {
			e.mu.Lock()
			e.reservedCount--
			e.reservedBytes -= size
			e.mu.Unlock()
		})
	}
	queued := reservedRecord{record: record, release: release}
	select {
	case e.records <- queued:
		e.accepted.Add(1)
		return true
	default:
		e.reservedCount--
		e.reservedBytes -= size
		e.rejected.Add(1)
		return false
	}
}

func (e *Exporter) Stats() ExporterStats {
	if e == nil {
		return ExporterStats{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return ExporterStats{ReservedRecords: e.reservedCount, ReservedBytes: e.reservedBytes}
}

func (e *Exporter) RecordLocalDrop(reason LocalDropReason) {
	if e == nil {
		return
	}
	switch reason {
	case LocalDropEventTooLarge:
		e.eventTooLarge.Add(1)
	case LocalDropEventEncodingFailed:
		e.eventEncodingFailed.Add(1)
	}
}

func (e *Exporter) Diagnostics() ExporterDiagnostics {
	if e == nil {
		return ExporterDiagnostics{}
	}
	return ExporterDiagnostics{
		Accepted:            e.accepted.Load(),
		Rejected:            e.rejected.Load(),
		Delivered:           e.delivered.Load(),
		Failed:              e.failed.Load(),
		EventTooLarge:       e.eventTooLarge.Load(),
		EventEncodingFailed: e.eventEncodingFailed.Load(),
	}
}

func (e *Exporter) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	var flushErr error
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.accepting = false
		close(e.records)
		e.mu.Unlock()

		<-e.workerFinished
		if err := e.sink.Flush(ctx); err != nil && flushErr == nil {
			flushErr = err
		}
		e.sink.Close()
		e.logAggregate("shutdown")
	})
	return flushErr
}

func (e *Exporter) run() {
	defer close(e.workerFinished)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case queued, ok := <-e.records:
			if !ok {
				return
			}
			release := queued.release
			e.sink.Produce(queued.record, func(err error) {
				release()
				if err != nil {
					e.failed.Add(1)
					e.logDeliveryFailure(err)
					return
				}
				e.delivered.Add(1)
			})
		case <-ticker.C:
			e.logAggregate("periodic")
		}
	}
}

func (e *Exporter) logDeliveryFailure(err error) {
	e.diagnosticMu.Lock()
	defer e.diagnosticMu.Unlock()
	now := time.Now()
	if now.Sub(e.lastFailureDiagnostic) < time.Minute {
		return
	}
	e.lastFailureDiagnostic = now
	log.Printf("Request logging delivery failed; continuing fail-open: %v", err)
}

func (e *Exporter) logAggregate(reason string) {
	diagnostics := e.Diagnostics()
	if diagnostics == (ExporterDiagnostics{}) {
		return
	}
	log.Printf(
		"Request logging exporter totals (%s): accepted=%d rejected=%d delivered=%d failed=%d event_too_large=%d event_encoding_failed=%d",
		reason,
		diagnostics.Accepted,
		diagnostics.Rejected,
		diagnostics.Delivered,
		diagnostics.Failed,
		diagnostics.EventTooLarge,
		diagnostics.EventEncodingFailed,
	)
}
