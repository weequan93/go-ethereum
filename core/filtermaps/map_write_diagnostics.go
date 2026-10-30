// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under the GNU LGPL v3.

package filtermaps

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

const (
	writeLockWait = iota
	writeRowWork
	writeBaseReadDecode
	writeEncodeEnqueue
	writeBatchCommit
	writePause
	writePhaseCount
)

const (
	writeRowsVisited = iota
	writeMapRowsSelected
	writeGroups
	writeHasCalls
	writeGetCalls
	writePutCalls
	writeDeleteCalls
	writeBatchCalls
	writeBatchZeroValueSize
	writeBatchValueBytes
	writeDetailClockCalls
	writeCountCount
)

// A record owns numbers only. Reader/writer wrappers never leave the indexer.
// Base preparation/read/decode and encode/enqueue are children of row_work.
// Has/Get/Put/Delete retain exact counts without per-call clock reads.
type mapWriteRecord struct {
	sequence, startedUnixNS, finishedUnixNS int64
	first, after, uncached                  uint32
	completed                               bool
	wall, active                            time.Duration
	times                                   [writePhaseCount]time.Duration
	counts                                  [writeCountCount]uint64
}

type mapWriteObservation struct {
	mapWriteRecord
	started     time.Time
	rowStarted  time.Time
	rowExcluded time.Duration
	reader      mapWriteReader
	writer      mapWriteWriter
}

// Cache peeks do not alter eviction order. Cached writes allocate no record
// and execute no per-row clock reads. enabled requires explicit per-indexer
// opt-in and the metrics system; request tracing never enables write diagnostics.
func newMapWriteObservation(enabled bool, f *FilterMaps, first, after uint32, started time.Time) *mapWriteObservation {
	if !enabled {
		return nil
	}
	var uncached uint32
	for index := first; index < after; index++ {
		if fm, _ := f.filterMapCache.Peek(index); fm == nil {
			uncached++
		}
	}
	if uncached == 0 {
		return nil
	}
	o := &mapWriteObservation{started: started}
	o.sequence, o.startedUnixNS = mapWriteSequence.Add(1), started.UnixNano()
	o.first, o.after, o.uncached = first, after, uncached
	o.reader = mapWriteReader{db: f.db, observation: o}
	o.writer.observation = o
	return o
}

func (o *mapWriteObservation) begin() time.Time {
	if o == nil {
		return time.Time{}
	}
	o.counts[writeDetailClockCalls]++
	return time.Now()
}

func (o *mapWriteObservation) end(phase int, started time.Time) {
	if o != nil {
		o.times[phase] += o.begin().Sub(started)
	}
}

// The complete row loop is timed once. Subtract only callbacks, commits and
// lock reacquisition inside this scope; metadata commits outside it remain
// separate. All rows and all groups are covered, without sampling.
func (o *mapWriteObservation) beginRows() {
	if o == nil {
		return
	}
	o.rowStarted = o.begin()
	o.rowExcluded = o.times[writePause] + o.times[writeBatchCommit] + o.times[writeLockWait]
}

func (o *mapWriteObservation) endRows() {
	if o == nil || o.rowStarted.IsZero() {
		return
	}
	elapsed := o.begin().Sub(o.rowStarted)
	excluded := o.times[writePause] + o.times[writeBatchCommit] + o.times[writeLockWait] - o.rowExcluded
	if elapsed > excluded {
		o.times[writeRowWork] += elapsed - excluded
	}
	o.rowStarted = time.Time{}
}

func (o *mapWriteObservation) commit(batch ethdb.Batch) error {
	if o == nil {
		return batch.Write()
	}
	size := batch.ValueSize()
	o.counts[writeBatchCalls]++
	if size == 0 {
		o.counts[writeBatchZeroValueSize]++
	}
	if size > 0 {
		o.counts[writeBatchValueBytes] += uint64(size)
	}
	started := o.begin()
	err := batch.Write()
	o.end(writeBatchCommit, started)
	return err
}

func (o *mapWriteObservation) finish(active time.Duration, completed bool) mapWriteRecord {
	finished := o.begin()
	o.active, o.completed, o.wall = active, completed, finished.Sub(o.started)
	o.finishedUnixNS = finished.UnixNano()
	return o.mapWriteRecord
}

type mapWriteReader struct {
	db          ethdb.KeyValueReader
	observation *mapWriteObservation
}

func (r *mapWriteReader) Has(key []byte) (bool, error) {
	r.observation.counts[writeHasCalls]++
	value, err := r.db.Has(key)
	return value, err
}

func (r *mapWriteReader) Get(key []byte) ([]byte, error) {
	r.observation.counts[writeGetCalls]++
	value, err := r.db.Get(key)
	return value, err
}

type mapWriteWriter struct {
	target      ethdb.KeyValueWriter
	observation *mapWriteObservation
}

func (w *mapWriteWriter) Put(key, value []byte) error {
	w.observation.counts[writePutCalls]++
	err := w.target.Put(key, value)
	return err
}

func (w *mapWriteWriter) Delete(key []byte) error {
	w.observation.counts[writeDeleteCalls]++
	err := w.target.Delete(key)
	return err
}

func (r mapWriteRecord) fields() []any {
	completed := 0
	if r.completed {
		completed = 1
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	fields := []any{"timing_schema", 2, "write_seq", r.sequence, "write_started_unix_ns", r.startedUnixNS, "write_finished_unix_ns", r.finishedUnixNS,
		"first_map", r.first, "after_last_map", r.after, "uncached_maps", r.uncached,
		"completed", completed, "wall_ms", ms(r.wall), "active_ms", ms(r.active)}
	for i, name := range []string{"lock_wait", "row_work", "base_prepare_read_decode", "encode_enqueue", "batch_commit", "pause_callback"} {
		fields = append(fields, name+"_ms", ms(r.times[i]))
	}
	for i, name := range []string{"rows_visited", "map_rows_selected", "row_groups", "has_calls", "get_calls", "row_put_calls", "row_delete_calls", "batch_calls", "batch_zero_value_size", "batch_value_bytes", "detail_clock_calls"} {
		fields = append(fields, name, r.counts[i])
	}
	// These residuals are elapsed work, including allocations and scheduling;
	// they are neither CPU time nor a physical-I/O latency measurement.
	residual := func(parent time.Duration, children ...time.Duration) time.Duration {
		for _, child := range children {
			parent -= child
		}
		if parent < 0 {
			return 0
		}
		return parent
	}
	return append(fields, "row_prepare_other_ms", ms(residual(r.times[writeRowWork], r.times[writeBaseReadDecode], r.times[writeEncodeEnqueue])),
		"other_active_ms", ms(residual(r.active, r.times[writeLockWait], r.times[writeRowWork], r.times[writeBatchCommit])))
}

// offer never waits on disk/log I/O. Rate and queue losses have separate
// counters; neither loss changes indexing, locks, cancellation or results.
type mapWriteQueue struct {
	records chan mapWriteRecord
	next    atomic.Int64
	limited atomic.Uint64
	full    atomic.Uint64
}

func (q *mapWriteQueue) offer(record mapWriteRecord, now int64, interval time.Duration) bool {
	for {
		next := q.next.Load()
		if now < next {
			q.limited.Add(1)
			return false
		}
		if q.next.CompareAndSwap(next, now+int64(interval)) {
			break
		}
	}
	select {
	case q.records <- record:
		return true
	default:
		q.full.Add(1)
		return false
	}
}

var (
	mapWriteSequence  atomic.Int64
	mapWriteInitOnce  sync.Once
	mapWriteQueueOnce sync.Once
	mapWriteLogQueue  mapWriteQueue
	mapWriteObserved  *metrics.Counter
	mapWriteSlow      *metrics.Counter
)

func emitMapWriteRecord(record mapWriteRecord) {
	// Never allocate the optional queue or register these counters at package
	// initialization. Only an explicitly enabled observed write reaches here.
	mapWriteInitOnce.Do(func() {
		mapWriteLogQueue.records = make(chan mapWriteRecord, 16)
		mapWriteObserved = metrics.NewRegisteredCounter("filtermaps/maps/write_diagnostics/observed", nil)
		mapWriteSlow = metrics.NewRegisteredCounter("filtermaps/maps/write_diagnostics/slow", nil)
	})
	mapWriteObserved.Inc(1)
	if record.active < time.Second && record.wall < time.Second {
		return
	}
	mapWriteSlow.Inc(1)
	mapWriteQueueOnce.Do(func() {
		go func() {
			for record := range mapWriteLogQueue.records {
				fields := append(record.fields(), "rate_limited_total", mapWriteLogQueue.limited.Load(), "queue_full_total", mapWriteLogQueue.full.Load())
				log.Warn("Log index write timings", fields...)
			}
		}()
	})
	mapWriteLogQueue.offer(record, time.Now().UnixNano(), 30*time.Second)
}

// The observed storage functions below are generated from the unchanged
// functions in filtermaps.go. Stripping the measurement lines must reproduce
// those exact bodies. All DB calls, data, order and returned errors are kept.
// GENERATED OBSERVED STORAGE
func (f *FilterMaps) storeFilterMapRowsObserved(batch ethdb.Batch, mapIndices []uint32, rowIndex uint32, rows []FilterRow, observation *mapWriteObservation) error {
	for len(mapIndices) > 0 {
		var (
			pos        = 1
			groupIndex = f.mapGroupIndex(mapIndices[0])
		)
		for pos < len(mapIndices) && f.mapGroupIndex(mapIndices[pos]) == groupIndex {
			pos++
		}
		if err := f.storeFilterMapRowsOfGroupObserved(batch, mapIndices[:pos], rowIndex, rows[:pos], observation); err != nil {
			return err
		}
		mapIndices, rows = mapIndices[pos:], rows[pos:]
	}
	return nil
}

// storeFilterMapRowsOfGroup stores a set of filter map rows at map indices
// belonging to the same base row group.
func (f *FilterMaps) storeFilterMapRowsOfGroupObserved(batch ethdb.Batch, mapIndices []uint32, rowIndex uint32, rows []FilterRow, observation *mapWriteObservation) error {
	// mapwrite-begin:group-start
	groupStarted := observation.begin()
	// mapwrite-end:group-start
	var (
		baseRows    [][]uint32
		groupIndex  = f.mapGroupIndex(mapIndices[0])
		mapRowIndex = f.mapRowIndex(groupIndex, rowIndex)
	)
	if uint32(len(mapIndices)) != f.baseRowGroupSize { // skip base rows read if all rows are replaced
		if f.baseRowAbsence.knownAbsent(groupIndex, rowIndex) {
			baseRows = make([][]uint32, f.baseRowGroupSize)
		} else {
			var err error
			baseRows, err = rawdb.ReadFilterMapBaseRows(&observation.reader, mapRowIndex, f.baseRowGroupSize, f.logMapWidth)
			if err != nil {
				// mapwrite-begin:base-error
				observation.times[writeBaseReadDecode] += observation.begin().Sub(groupStarted)
				// mapwrite-end:base-error
				return fmt.Errorf("failed to retrieve filter map %d base rows %d for modification: %v", groupIndex, rowIndex, err)
			}
		}
	} else {
		baseRows = make([][]uint32, f.baseRowGroupSize)
	}
	// mapwrite-begin:encode-start
	readFinished := observation.begin()
	observation.times[writeBaseReadDecode] += readFinished.Sub(groupStarted)
	observation.counts[writeGroups]++
	observation.writer.target = batch
	// mapwrite-end:encode-start
	for i, mapIndex := range mapIndices {
		if f.mapGroupIndex(mapIndex) != groupIndex {
			return fmt.Errorf("maps are not in the same base row group, index: %d, group: %d", mapIndex, groupIndex)
		}
		baseRow := []uint32(rows[i])
		var extRow FilterRow
		if uint32(len(rows[i])) > f.baseRowLength {
			extRow = baseRow[f.baseRowLength:]
			baseRow = baseRow[:f.baseRowLength]
		}
		baseRows[f.mapGroupOffset(mapIndex)] = baseRow
		rawdb.WriteFilterMapExtRow(&observation.writer, f.mapRowIndex(mapIndex, rowIndex), extRow, f.logMapWidth)
	}
	rawdb.WriteFilterMapBaseRows(&observation.writer, mapRowIndex, baseRows, f.logMapWidth)
	f.baseRowAbsence.update(groupIndex, rowIndex, baseRows)
	// mapwrite-begin:encode-end
	observation.times[writeEncodeEnqueue] += observation.begin().Sub(readFinished)
	// mapwrite-end:encode-end
	return nil
}
