# uped

LAN file drop: a Go static binary serving one web page where any device on the
home network uploads files or folders and any other device downloads them. No
auth, plain HTTP, items expire after 7 days. Target host is an Alpine LXC on
Proxmox, installed with one `curl | sh` / `wget -qO- | sh` command.

**The plan is the source of truth:** `plans/uped-home-file-drop.md`. It holds
the locked decisions, the HTTP API and on-disk layout, and the phase checklist.
Tick checkboxes, set phase status, and write the Phase Summary as you go.
Background research is in `plans/research/`.

## Commands

```sh
make build    # static binary at dist/uped (CGO_ENABLED=0)
make test     # go vet ./... && go test -race ./...
make run      # serve on 127.0.0.1:8080 from ./data (LISTEN=0.0.0.0:8080 to test from a phone)
make cross    # dist/uped_linux_{amd64,arm64}.tar.gz + dist/checksums.txt
sh scripts/smoke.sh   # end-to-end curl check of dist/uped; prints SMOKE OK
make e2e      # Playwright suite in e2e/ (Phase 4)
dist/uped --help
```

## Layout

- `cmd/uped/` flags with `UPED_*` env fallback (`config.go`), server start and shutdown (`main.go`).
- `internal/names/` pure helpers: sanitise names, dedupe `name (1).ext`, device label from User-Agent.
- `internal/store/` everything on disk under `--data`: chunked uploads, list, delete, zip, sweeper, disk guard.
- `internal/server/` HTTP handlers and the SSE hub. Static files arrive via `server.Options.Static`.
  Error-to-status mapping lives in `respond.go`; idle deadlines and request logging in `middleware.go`.
- `web/` the UI. `web/embed.go` embeds it as `web.FS`; add new asset files to its `//go:embed` line.
  `go:embed` cannot reach parent directories, which is why the embed lives in `web/` itself.

## Rules

- **No third-party Go modules.** Standard library only; `go.mod` must have no `require` lines.
- **Go 1.24 compatible.** `go.mod` says `go 1.24`, matching Alpine 3.22's `apk add go` and this
  container. Do not use APIs newer than 1.24 (for example `os.Root.Rename` is 1.25) unless the
  plan is updated to bump the version.
- **Frontend is vanilla HTML/CSS/JS** with no build step and no third-party JS.
- **The page is served over plain HTTP on a LAN IP**, which is not a secure context. Never rely on
  service workers, `navigator.clipboard` (use the `execCommand('copy')` fallback), Web Share,
  `crypto.subtle` or `crypto.randomUUID`. Use `XMLHttpRequest` for uploads, not streaming `fetch`.
- **HTTP server timeouts:** keep `ReadTimeout` and `WriteTimeout` at zero; long uploads, zip
  streams and SSE depend on it. Bound work per request with `http.ResponseController` deadlines.
- **Every user-supplied path** goes through `internal/names` sanitising, `filepath.IsLocal`, and
  `os.Root` I/O. Never join a client path onto the data dir directly.
- **Scripts shipped to users are POSIX `sh`.** Alpine's LXC template has busybox `ash` and `wget`,
  no bash and no curl. Check with `sh -n`. No bash-isms.
- **Playwright:** browsers are preinstalled at `/opt/pw-browsers`. Never run `playwright install`
  locally; pin `@playwright/test` to the version matching that directory. CI installs its own.
- **No Docker daemon** in cloud sessions: Dockerfile changes are verified by CI.
