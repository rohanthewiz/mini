// Client script, authored in TypeScript. esbuild (embedded in the Go binary)
// strips the type annotations and minifies at server startup — see assets.go.
// Note esbuild does type *stripping*, not type *checking*; run tsc/tsgo in CI
// if full checking is ever wanted.

// Mirror of the JSON shape served by /api/status (web/handlers.go).
interface Status {
  response: string
  env: string
  visits: number
}

const refreshMs = 5000

// Poll the status endpoint and keep the visit counter on the page current.
// Polling (vs SSE/WebSocket) is deliberate here: the update is low-value and
// low-frequency, so the simplest transport wins.
async function refreshStatus(): Promise<void> {
  try {
    const res = await fetch("/api/status")
    if (!res.ok) return
    const st = (await res.json()) as Status
    const el = document.getElementById("live-visits")
    if (el) el.textContent = String(st.visits)
  } catch {
    // Transient network errors just skip a beat; the next tick retries.
  }
}

// The script tag is loaded with `defer`, so the DOM is already parsed when
// this runs — no DOMContentLoaded dance needed.
refreshStatus()
setInterval(refreshStatus, refreshMs)
