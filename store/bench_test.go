package store

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/bytdb"
)

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
func BenchmarkRecordVisitSustainedDurable(b *testing.B) {
	st, err := Open(filepath.Join(b.TempDir(), "bench.db"))
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
