package web

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/rohanthewiz/bytdb"

	"mini/store"
)

// benchGet drives sequential GETs at path and fails fast on any non-200.
// Sequential requests measure per-request latency; the *Parallel benchmarks
// below measure throughput under concurrency.
func benchGet(b *testing.B, base, path string) {
	b.Helper()

	for b.Loop() {
		resp, err := http.Get(base + path)
		if err != nil {
			b.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status = %d", resp.StatusCode)
		}
		// Drain so the transport can reuse the connection — otherwise we
		// would be benchmarking TCP handshakes.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// seedVisits inserts rows directly through the store (not HTTP), riding out
// queue saturation and flushing at the end so all rows are committed before
// measurement starts.
func seedVisits(b *testing.B, st *store.Store, rows int) {
	b.Helper()
	for range rows {
		if err := st.RecordVisit("/seed"); err != nil {
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

// --- No-DB baselines: what the framework + asset path cost by themselves ---

func BenchmarkHTTPHealth(b *testing.B) {
	base, _ := startTestServer(b)
	benchGet(b, base, "/health")
}

func BenchmarkHTTPAssetCSS(b *testing.B) {
	base, _ := startTestServer(b)
	benchGet(b, base, "/assets/app.css")
}

// --- Read-only DB endpoint at increasing table sizes ---

func BenchmarkHTTPStatus(b *testing.B) {
	for _, rows := range []int{0, 1000, 10000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			base, st := startTestServer(b, bytdb.WithSyncNever())
			seedVisits(b, st, rows)
			benchGet(b, base, "/api/status")
		})
	}
}

// --- Full page: DB write + count scan + recent scan + HTML render ---

// Durable sync (production default): the per-request fsync dominates.
func BenchmarkHTTPRootDurable(b *testing.B) {
	base, _ := startTestServer(b)
	benchGet(b, base, "/")
}

// Sync disabled: everything except the fsync — engine + render + HTTP cost.
func BenchmarkHTTPRootSyncNever(b *testing.B) {
	for _, rows := range []int{0, 10000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			base, st := startTestServer(b, bytdb.WithSyncNever())
			seedVisits(b, st, rows)
			benchGet(b, base, "/")
		})
	}
}

// --- Throughput under concurrency ---

// benchGetParallel drives concurrent GETs; ns/op then reflects aggregate
// throughput (wall time / total requests), not individual latency.
func benchGetParallel(b *testing.B, base, path string) {
	b.Helper()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := http.Get(base + path)
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// Concurrent durable writes reveal whether commits queue serially behind the
// fsync or amortize it across requests (group commit).
func BenchmarkHTTPRootParallelDurable(b *testing.B) {
	base, _ := startTestServer(b)
	benchGetParallel(b, base, "/")
}

func BenchmarkHTTPRootParallelSyncNever(b *testing.B) {
	base, _ := startTestServer(b, bytdb.WithSyncNever())
	benchGetParallel(b, base, "/")
}
