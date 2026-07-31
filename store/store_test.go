package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()

	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRecordAndCount(t *testing.T) {
	st := openTestStore(t)

	for range 3 {
		if err := st.RecordVisit("/"); err != nil {
			t.Fatalf("record visit: %v", err)
		}
	}

	// The counter includes queued (not yet committed) visits by design —
	// no Flush needed for the count to be visible.
	if count := st.VisitCount(); count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}

func TestRecentVisitsNewestFirst(t *testing.T) {
	st := openTestStore(t)

	paths := []string{"/a", "/b", "/c"}
	for _, p := range paths {
		if err := st.RecordVisit(p); err != nil {
			t.Fatalf("record visit %s: %v", p, err)
		}
	}
	// Writes are async; the scan only sees committed rows, so barrier first.
	if err := st.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	recent, err := st.RecentVisits(2)
	if err != nil {
		t.Fatalf("recent visits: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("len(recent) = %d, want 2 (limit respected)", len(recent))
	}
	// Reverse primary-key order: the last insert comes back first.
	if recent[0].Path != "/c" || recent[1].Path != "/b" {
		t.Fatalf("recent order = [%s, %s], want [/c, /b]", recent[0].Path, recent[1].Path)
	}
	if recent[0].ID <= recent[1].ID {
		t.Fatalf("ids not descending: %d then %d", recent[0].ID, recent[1].ID)
	}
	if recent[0].At.IsZero() {
		t.Fatal("visit timestamp is zero")
	}
}

// TestBatchedWritesAllLand pushes enough visits to span several batches and
// verifies every one of them is committed after a flush.
func TestBatchedWritesAllLand(t *testing.T) {
	st := openTestStore(t)

	const visits = maxBatch*2 + 7 // deliberately not a multiple of the batch size
	for range visits {
		if err := st.RecordVisit("/burst"); err != nil {
			t.Fatalf("record visit: %v", err)
		}
	}
	if err := st.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Count the committed rows directly (bypassing the atomic counter) so
	// this asserts what actually reached disk.
	rows, err := st.RecentVisits(visits + 10)
	if err != nil {
		t.Fatalf("recent visits: %v", err)
	}
	if len(rows) != visits {
		t.Fatalf("committed rows = %d, want %d", len(rows), visits)
	}
}

// TestSchemaSurvivesReopen ensures the second Open of the same file takes the
// "table already exists" path, reseeds the counter from disk, and can still
// read prior data.
func TestSchemaSurvivesReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err = st.RecordVisit("/persist"); err != nil {
		t.Fatalf("record visit: %v", err)
	}
	// Close drains the write queue, so no explicit Flush is needed.
	if err = st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()

	if count := st2.VisitCount(); count != 1 {
		t.Fatalf("count after reopen = %d, want 1", count)
	}
}

// TestClosedStoreRejectsWrites pins the shutdown contract: after Close, both
// RecordVisit and Flush refuse cleanly instead of hanging or panicking.
func TestClosedStoreRejectsWrites(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err = st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err = st.RecordVisit("/late"); err == nil {
		t.Fatal("RecordVisit after Close should error")
	}
	if err = st.Flush(); err == nil {
		t.Fatal("Flush after Close should error")
	}
}
