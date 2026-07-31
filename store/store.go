// Package store persists app data in bytdb — an embedded, pure-Go relational
// engine over a single ordered keyspace (SQLite's niche, no cgo). The demo
// schema is a single "visits" table.
//
// Two latency measures shape this package (see bench_test.go):
//
//   - A durable commit fsyncs, costing ~5ms — so visits are not written on
//     the caller's goroutine. RecordVisit enqueues to a single writer
//     goroutine that folds queued visits into one transaction: one fsync
//     amortized over up to maxBatch rows.
//   - Counting by scanning is O(rows) — so the count lives in an atomic,
//     seeded by one scan at Open and bumped as visits are accepted.
//
//	RecordVisit ──> msgCh ──> writer goroutine ──> one Txn per batch ──> fsync
//	VisitCount  ──> atomic load (no engine touch)
package store

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rohanthewiz/btypedb"
	"github.com/rohanthewiz/bytdb"
	"github.com/rohanthewiz/logger"
	"github.com/rohanthewiz/serr"
)

// Option re-exports the underlying engine option type so callers can tune the
// engine (e.g. bytdb.WithSyncNever() for benchmarks/bulk loads) without
// importing btypedb themselves.
type Option = btypedb.Option

const (
	visitsTable = "visits"
	// visitSeq is a bytdb named sequence: monotonic ids without us having to
	// read-modify-write a counter row (the engine owns the atomicity). The
	// writer allocates from it inside the batch transaction, so the sequence
	// bump and the inserts share a single commit.
	visitSeq = "visit_id"

	// queueDepth bounds memory while the writer is mid-commit: a durable
	// batch fsync takes ~5ms, and 1024 slots comfortably hold what arrives
	// in that window (measured sustained durable throughput is ~69k
	// visits/sec) before RecordVisit starts shedding.
	queueDepth = 1024

	// maxBatch caps rows per transaction so one commit's fsync is amortized
	// widely, without letting a single transaction grow unboundedly under
	// sustained load.
	maxBatch = 256
)

// Visit is one recorded page view, decoded from a visits row.
type Visit struct {
	ID   int64
	At   time.Time
	Path string
}

// pendingVisit is a visit accepted but not yet committed. The timestamp is
// stamped at accept time (not commit time) so batching never skews it; the id
// is allocated by the writer inside the batch transaction.
type pendingVisit struct {
	atMicros int64
	path     string
}

// flushReq is a barrier message: the writer closes it once every message
// enqueued before it has been committed. FIFO channel order is what makes
// that guarantee hold.
type flushReq chan struct{}

// stopMsg tells the writer to commit what it holds and exit.
type stopMsg struct{}

// Store wraps the bytdb engine behind app-level methods so handlers never
// touch tables or column names directly.
type Store struct {
	eng *bytdb.Engine

	// visitCount mirrors the table's row count in memory: seeded by one scan
	// at Open, incremented when a visit is accepted, decremented if its batch
	// later fails to commit. Reads are a single atomic load — O(1) at any
	// table size, where a counting scan was ~160ns/row.
	visitCount atomic.Int64

	msgCh      chan any // carries pendingVisit, flushReq, or stopMsg
	writerDone chan struct{}
	closed     atomic.Bool
	closeOnce  sync.Once
}

// Open opens (or creates) the database file, ensures the schema exists, seeds
// the in-memory count, and starts the batch writer.
func Open(path string, opts ...Option) (*Store, error) {
	eng, err := bytdb.Open(path, opts...)
	if err != nil {
		return nil, serr.Wrap(err, "opening bytdb at "+path)
	}

	// The descriptor catalog lives inside the database file, so the table
	// survives restarts; CreateTable is not idempotent, hence the existence
	// check. Primary key is the sequence-issued id — inserts are naturally
	// append-ordered, which is what makes "recent visits" a cheap reverse
	// scan with no secondary index.
	if eng.Table(visitsTable) == nil {
		_, err = eng.CreateTable(visitsTable, []bytdb.Column{
			{Name: "id", Type: bytdb.TInt, NotNull: true},
			{Name: "at", Type: bytdb.TTimestamp, NotNull: true},
			{Name: "path", Type: bytdb.TString, NotNull: true},
		}, "id")
		if err != nil {
			_ = eng.Close()
			return nil, serr.Wrap(err, "creating visits table")
		}
	}

	st := &Store{
		eng:        eng,
		msgCh:      make(chan any, queueDepth),
		writerDone: make(chan struct{}),
	}

	// Seed the counter with the one full scan we still pay — once per
	// process, not once per request.
	seeded := 0
	for _, scanErr := range eng.Scan(visitsTable) {
		if scanErr != nil {
			_ = eng.Close()
			return nil, serr.Wrap(scanErr, "seeding visit count")
		}
		seeded++
	}
	st.visitCount.Store(int64(seeded))

	go st.writer()

	return st, nil
}

// RecordVisit accepts a visit for asynchronous persistence and returns
// immediately — the caller never waits on a commit. If the queue is full
// (writer saturated), the visit is shed and an error returned; page views are
// analytics-grade data, so callers may reasonably log and continue.
func (s *Store) RecordVisit(path string) error {
	if s.closed.Load() {
		return serr.New("store is closed")
	}

	select {
	case s.msgCh <- pendingVisit{atMicros: time.Now().UnixMicro(), path: path}:
		// Count on accept, not on commit: the page that triggered this visit
		// renders before the batch lands, and it should include itself.
		s.visitCount.Add(1)
		return nil
	default:
		return serr.New("visit queue full; visit dropped")
	}
}

// Flush blocks until every visit enqueued before the call is committed.
// Used by tests and anywhere read-your-write on the table itself matters.
func (s *Store) Flush() error {
	if s.closed.Load() {
		return serr.New("store is closed")
	}

	barrier := make(flushReq)
	s.msgCh <- barrier
	<-barrier
	return nil
}

// writer is the single goroutine that owns table writes. It greedily drains
// the queue into a batch (never blocking once it holds work), then commits
// the whole batch in one transaction — so under load, one fsync covers up to
// maxBatch visits, and when idle, a lone visit still commits promptly.
func (s *Store) writer() {
	defer close(s.writerDone)

	pending := make([]pendingVisit, 0, maxBatch)
	var barriers []flushReq

	// commit writes the held batch (if any) and releases any flush barriers.
	// A failed commit sheds the batch: the error is logged and the counter
	// walked back so VisitCount stays honest.
	commit := func() {
		if len(pending) > 0 {
			if err := s.commitBatch(pending); err != nil {
				logger.LogErr(err, "committing visit batch",
					"batchSize", strconv.Itoa(len(pending)))
				s.visitCount.Add(-int64(len(pending)))
			}
			pending = pending[:0]
		}
		for _, barrier := range barriers {
			close(barrier)
		}
		barriers = barriers[:0]
	}

	for msg := range s.msgCh {
		// Absorb this message plus everything immediately available, up to
		// the batch cap. FIFO order means a barrier releases only after the
		// visits enqueued before it are part of the committed batch.
		for {
			switch m := msg.(type) {
			case pendingVisit:
				pending = append(pending, m)
			case flushReq:
				barriers = append(barriers, m)
			case stopMsg:
				commit()
				return
			}

			if len(pending) >= maxBatch {
				break
			}
			select {
			case msg = <-s.msgCh:
				continue
			default:
			}
			break
		}

		commit()
	}
}

// commitBatch writes one batch atomically: sequence allocations and inserts
// share the transaction, so the whole batch costs a single fsync.
func (s *Store) commitBatch(batch []pendingVisit) error {
	tx, err := s.eng.Begin(true)
	if err != nil {
		return serr.Wrap(err, "beginning visit batch txn")
	}

	for _, v := range batch {
		id, seqErr := tx.NextSeq(visitSeq)
		if seqErr != nil {
			_ = tx.Rollback()
			return serr.Wrap(seqErr, "allocating visit id")
		}
		// TTimestamp columns store int64 microseconds since the Unix epoch
		// (UTC); the wall-clock conversion happened at accept time.
		if insErr := tx.Insert(visitsTable, int64(id), v.atMicros, v.path); insErr != nil {
			_ = tx.Rollback()
			return serr.Wrap(insErr, "inserting visit")
		}
	}

	if err = tx.Commit(); err != nil {
		return serr.Wrap(err, "committing visit batch")
	}
	return nil
}

// VisitCount reports the number of accepted visits — an atomic load, O(1) at
// any table size. It includes visits still queued for commit (see
// RecordVisit); the walk-back on a failed batch keeps it eventually exact.
func (s *Store) VisitCount() int {
	return int(s.visitCount.Load())
}

// RecentVisits returns up to limit committed visits, newest first. Because
// the primary key is the monotonically increasing id, "newest first" is just
// a reverse primary-key scan (nil bounds = whole table) cut off at limit —
// no index, no sort. Visits still in the write queue are not yet visible;
// call Flush first when that matters.
func (s *Store) RecentVisits(limit int) (visits []Visit, err error) {
	for row, scanErr := range s.eng.ScanRangeRev(visitsTable, nil, nil, false) {
		if scanErr != nil {
			return nil, serr.Wrap(scanErr, "scanning recent visits")
		}

		// Column value types follow the schema: TInt/TTimestamp decode to
		// int64, TString to string. The soft assertions (ok-form) mean a
		// future schema change degrades to zero values instead of panicking
		// mid-request.
		id, _ := row.Col("id").(int64)
		atMicros, _ := row.Col("at").(int64)
		pagePath, _ := row.Col("path").(string)

		visits = append(visits, Visit{
			ID:   id,
			At:   time.UnixMicro(atMicros).UTC(),
			Path: pagePath,
		})
		if len(visits) >= limit {
			break
		}
	}
	return visits, nil
}

// Close stops accepting visits, waits for the writer to commit everything
// already queued, then closes the engine. Called via the shutdown service so
// in-flight writes settle before process exit.
func (s *Store) Close() (err error) {
	s.closeOnce.Do(func() {
		// Order matters: refuse new visits first, then send the sentinel.
		// FIFO delivery means the writer sees (and commits) every visit that
		// was accepted before the sentinel. A racing RecordVisit that slips
		// in between the flag check and the send can at worst enqueue behind
		// the sentinel and be dropped — at process exit, an acceptable loss
		// for analytics data.
		s.closed.Store(true)
		s.msgCh <- stopMsg{}
		<-s.writerDone

		if closeErr := s.eng.Close(); closeErr != nil {
			err = serr.Wrap(closeErr, "closing bytdb engine")
		}
	})
	return err
}
