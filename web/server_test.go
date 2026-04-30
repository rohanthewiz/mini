package web

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/rohanthewiz/rweb"
)

// startTestServer spins up the server on a random port and returns its base URL.
func startTestServer(t *testing.T) string {
	t.Helper()

	ready := make(chan struct{}, 1)
	s := newServer(rweb.ServerOptions{
		Address:   ":0",
		ReadyChan: ready,
	})

	go func() {
		if err := s.Run(); err != nil {
			t.Logf("server.Run returned: %v", err)
		}
	}()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not become ready in time")
	}

	return "http://" + s.GetListenAddr()
}

func TestHealthEndpoint(t *testing.T) {
	base := startTestServer(t)

	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(body); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestRootEndpoint(t *testing.T) {
	base := startTestServer(t)

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if payload["response"] != "OK" {
		t.Fatalf(`payload["response"] = %v, want "OK"`, payload["response"])
	}
}