# Session: tsgo type-check for app.ts in CI

- Session ID: `fd412bda-cf58-4e98-bcd5-674893aaaa8d`
- Date: 2026-07-31
- Branch: `main`
- Continues: `2026-0731-1428-fingerprinted-asset-urls.md` (same session, fifth part)

## What was done

Closed out the last carried-over item. The repo had **no CI at all**, so this
created `.github/workflows/ci.yml` from scratch.

### 1. tsgo installs with Go — no Node in CI

```sh
go install github.com/microsoft/typescript-go/cmd/tsgo@<version>
tsgo --noEmit -p assets
```

tsgo is the **Go port** of the TypeScript compiler, so the check installs with
the Go the runner already has. That keeps the project's "no Node, no external
toolchain" property intact even in CI — the npm route
(`@typescript/native-preview`) would have broken it for no gain.

**The version is pinned, not `@latest`.** typescript-go publishes no semver
tags — `go install` resolves to a pseudo-version
(`v0.0.0-20260731182708-5b1047d10d32`) — so `@latest` would move under CI and
turn an upstream change into a failure on an unrelated PR. Both `@latest` and
the pinned pseudo-version were confirmed to install and run. The binary is
cached on that version key, so a hit skips the install entirely.

### 2. `assets/tsconfig.json`

Nothing in the build reads it: esbuild strips types without consulting a
tsconfig, so the file exists purely so tsgo can check what esbuild silently
accepts.

- `target: ES2020` deliberately mirrors `api.ES2020` in `assets.go` — checking
  against a newer target would admit syntax the shipped bundle cannot use.
- `lib: ["ES2020", "DOM"]`, `strict`, `noUnusedLocals`, `noUnusedParameters`,
  `noImplicitReturns`, `noFallthroughCasesInSwitch`,
  `forceConsistentCasingInFileNames`, `noEmit`, `skipLibCheck`.

**Gotcha:** tsgo **rejects `"module": "none"`** (tsc accepts it) —
`error TS6046: Argument for '--module' option must be: 'commonjs', 'es6',
'es2015', 'es2020', 'es2022', 'esnext', 'node16', 'node18', 'node20',
'nodenext', 'preserve'`. Dropped the setting entirely: `app.ts` has no imports
or exports, so TypeScript treats it as a classic script regardless, which is
what it is (loaded by a bare `<script defer>`).

### 3. Proved the check can actually fail

A type-check job that cannot fail is theater, so two errors were injected and
the file restored:

| injected | result |
|---|---|
| `String(st.visitz)` | `TS2551: Property 'visitz' does not exist on type 'Status'` — exit 1 |
| dropped the `if (el)` guard | `TS18047: 'el' is possibly 'null'` — exit 1 |
| restored | exit 0 |

The first is the exact drift this guards against: the `Status` interface in
`app.ts` mirrors `statusPayload` in `web/handlers.go`, and esbuild would ship
a typo'd field silently for the browser to fail on.

### 4. Workflow shape

Two jobs, on push to `main` and on every PR:

- **`go`** — `go build ./...`, `go vet ./...`, `go test -race ./...`.
  `-race` because the store's batch writer and the status cache are both
  concurrent by design.
- **`typecheck`** — cache tsgo → install if missed → `tsgo --noEmit -p assets`.
  Invoked as `$(go env GOPATH)/bin/tsgo` rather than relying on GOPATH/bin
  being on PATH, so the step cannot silently resolve to something else.

**Scope note (flagged to the user, not done quietly):** only the type check
was requested. The `go` job was added on the reasoning that a workflow named
"CI" going green without ever running the Go tests is actively misleading.
Easy to drop if unwanted.

## Verification

- `tsgo --noEmit -p assets` — clean, exit 0.
- `go build ./... && go vet ./... && go test -race ./...` — all green.
- Workflow YAML parsed structurally (no `yaml` module for python3, no `yq` or
  `actionlint` on this machine — used a throwaway Go program with
  `gopkg.in/yaml.v3`): both jobs present, 5 steps each, env var resolved.
- GitHub Actions itself cannot run locally, so the first real run is the push.

## Files changed

- `.github/workflows/ci.yml` — new; the whole CI setup
- `assets/tsconfig.json` — new; type-check config, unused by the build
- `assets/app.ts` — stale header comment ("run tsc/tsgo in CI if full checking
  is ever wanted") replaced with what actually happens now
- `README.md` — tsgo install/run commands in the front-end pipeline section;
  the local command and a line on CI coverage under tests

## Possible next steps

- Watch the first CI run and fix any runner-only fallout.
- Revisit `WithConcurrentWrites` only if independent writers ever contend
  (e.g. several unrelated tables writing in parallel).
- Optional: drop the unversioned asset routes once nothing references them,
  leaving only immutable fingerprinted URLs.
- Optional: a status badge in the README once CI has a green run.
