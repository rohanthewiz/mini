package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/rohanthewiz/bytdb"
	"github.com/rohanthewiz/serr"
)

// writeModes enumerates bytdb's two write modes (v0.7.0+) so the write-path
// benchmarks can be run under both and the choice made on numbers:
//
//   - single-writer (default): writable transactions serialize behind a
//     writer lock. Trivially serializable, sequence draws gapless on rollback.
//   - concurrent (OCC): writers build against private COW snapshots and
//     validate at commit; a loser gets bytdb.ErrTxConflict and re-runs.
//     Sequence draws become non-transactional (gaps on abort) so parallel
//     inserts don't all collide on the visits counter key.
//
// The store's own write path is a *single* writer goroutine, so it has no
// contention for OCC to relieve — these benchmarks quantify both that
// (no win, possible overhead) and what an unbatched per-request design
// would get instead (see BenchmarkVisitCommitDirectParallel).
var writeModes = []struct {
	name string
	opts []Option
}{
	{"single-writer", nil},
	{"concurrent", []Option{bytdb.WithConcurrentWrites()}},
}

// syncModes separates disk cost from transaction-machinery cost: durable is
// what production pays (fsync per commit), sync-never isolates the engine.
var syncModes = []struct {
	name string
	opts []Option
}{
	{"durable", nil},
	{"sync-never", []Option{bytdb.WithSyncNever()}},
}

// benchOpts flattens a mode/sync pair into engine options.
func benchOpts(sets ...[]Option) []Option {
	var opts []Option
	for _, set := range sets {
		opts = append(opts, set...)
	}
	return opts
}

// openBenchStore returns a store seeded with rows committed visits. It opens
// the engine with fsync disabled so seeding 10k rows takes milliseconds
// instead of minutes; sync mode has no effect on the read-path benchmarks
// that use this.
func openBenchStore(b *testing.B, rows int) *Store {
	b.Helper()

	st, err := Open(filepath.Join(b.TempDir(), "bench.db"), bytdb.WithSyncNever())
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	seedVisits(b, st, rows)
	return st
}

// seedVisits records rows visits, flushing whenever the queue saturates and
// once at the end so every row is committed before measurement starts.
func seedVisits(b *testing.B, st *Store, rows int) {
	b.Helper()
	for range rows {
		if err := st.RecordVisit("/seed"); err != nil {
			// Queue full: barrier on the writer, then the retry must fit.
			if err = st.Flush(); err != nil {
				b.Fatalf("flush during seed: %v", err)
			}
			if err = st.RecordVisit("/seed"); err != nil {
				b.Fatalf("seed visit after flush: %v", err)
			}
		}
	}
	if err := st.Flush(); err != nil {
		b.Fatalf("final seed flush: %v", err)
	}
}

// BenchmarkRecordVisitEnqueue is what a request now pays: a channel send and
// an atomic increment. The commit happens elsewhere. Sync-never engine so the
// background writer can absorb the firehose without minutes of fsync.
func BenchmarkRecordVisitEnqueue(b *testing.B) {
	st := openBenchStore(b, 0)

	for b.Loop() {
		if err := st.RecordVisit("/bench"); err != nil {
			// Writer briefly saturated — drain and continue.
			if err = st.Flush(); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkRecordVisitSustainedDurable measures fully durable sustained
// throughput: enqueue until the queue saturates, then wait for the writer's
// batched commits. ns/op is the amortized cost of one durable visit — the
// fsync divided across each batch.
//
// Run under both write modes to check whether opting into OCC costs the
// current design anything: the store commits from one goroutine, so OCC's
// snapshot copy and commit-time validation are pure overhead here with no
// contention to relieve.
func BenchmarkRecordVisitSustainedDurable(b *testing.B) {
	for _, mode := range writeModes {
		b.Run("mode="+mode.name, func(b *testing.B) {
			st, err := Open(filepath.Join(b.TempDir(), "bench.db"), mode.opts...)
			if err != nil {
				b.Fatalf("open store: %v", err)
			}
			b.Cleanup(func() { _ = st.Close() })

			for b.Loop() {
				if err := st.RecordVisit("/bench"); err != nil {
					if err = st.Flush(); err != nil {
						b.Fatal(err)
					}
					if err = st.RecordVisit("/bench"); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// commitVisitDirect writes one visit in its own transaction, bypassing the
// batch queue entirely — the unbatched shape a "just let every request write"
// design would have. Under WithConcurrentWrites a commit can lose a
// validation race, and the engine's contract puts that retry on the caller
// (only the caller knows the transaction is replayable). The attempt cap
// means a pathological livelock fails the benchmark rather than hanging it.
func commitVisitDirect(st *Store, path string) error {
	const maxAttempts = 10

	for range maxAttempts {
		err := st.eng.WriteTxn(func(tx *bytdb.Txn) error {
			id, seqErr := tx.NextSeq(visitSeq)
			if seqErr != nil {
				return serr.Wrap(seqErr, "allocating visit id")
			}
			return tx.Insert(visitsTable, int64(id), time.Now().UnixMicro(), path)
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, bytdb.ErrTxConflict) {
			return serr.Wrap(err, "committing visit")
		}
		// Lost the race: another writer committed a key this transaction
		// depended on. Re-run from a fresh snapshot.
	}
	return serr.New("visit commit conflicted " + strconv.Itoa(maxAttempts) + " times")
}

// BenchmarkVisitCommitDirectParallel measures the alternative that concurrent
// writes makes viable: 8 request goroutines each committing their own visit,
// no queue and no batching. Comparing it against the batched numbers above is
// the whole decision — OCC removes the writer lock, but it cannot remove the
// fsync, and batching amortizes exactly that.
func BenchmarkVisitCommitDirectParallel(b *testing.B) {
	for _, mode := range writeModes {
		for _, sync := range syncModes {
			b.Run("mode="+mode.name+"/"+sync.name, func(b *testing.B) {
				st, err := Open(filepath.Join(b.TempDir(), "bench.db"),
					benchOpts(mode.opts, sync.opts)...)
				if err != nil {
					b.Fatalf("open store: %v", err)
				}
				b.Cleanup(func() { _ = st.Close() })

				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						// b.Error, not b.Fatal: FailNow is only legal on the
						// goroutine running the benchmark.
						if err := commitVisitDirect(st, "/bench"); err != nil {
							b.Error(err)
							return
						}
					}
				})
			})
		}
	}
}

// BenchmarkVisitCount confirms the count is O(1) — flat across table sizes
// (it was ~160ns/row when it scanned).
func BenchmarkVisitCount(b *testing.B) {
	for _, rows := range []int{0, 1000, 10000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			st := openBenchStore(b, rows)

			for b.Loop() {
				if st.VisitCount() < rows {
					b.Fatal("count lost visits")
				}
			}
		})
	}
}

// BenchmarkRecentVisits shows the reverse scan is O(limit), independent of
// table size — it stops after 5 rows no matter how many exist.
func BenchmarkRecentVisits(b *testing.B) {
	for _, rows := range []int{100, 10000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			st := openBenchStore(b, rows)

			for b.Loop() {
				if _, err := st.RecentVisits(5); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
