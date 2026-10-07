# Uped: home LAN file drop

Build `uped`, a USB-stick replacement for a home network: one Go static binary that
serves a single web page; any device on the LAN opens it without logging in, drops
files or folders (chunked, resumable), and any other device downloads them. Items
expire after 7 days. Installable inside an Alpine Linux LXC on Proxmox with one
`curl | sh` command.

Scope was fixed in an interview on 2026-10-07 (see **Decisions**). Research that
backs the design lives in `plans/research/` and is referenced by section below.

## For Future Agents
As work proceeds: mark checkboxes `- [x]` as items complete; when a phase is done,
set its status to `Complete` and write its **Phase Summary** (what was done, key
decisions, anything needed to continue with zero context); run the phase's
**Verification Plan** and record the result before moving on. When all phases are
done, fill in **Final Recap** and **Deployment Plan**.

Work on branch `claude/home-file-transfer-app-9os7eu`. Commit per phase (or more
often), push with `git push -u origin claude/home-file-transfer-app-9os7eu`.
Do not open a pull request unless the user asks.

Environment facts for this repo's cloud sessions (checked 2026-10-07): Go 1.24.7,
Node 22, Playwright 1.56.1 with Chromium under `/opt/pw-browsers` (never run
`playwright install`), Docker CLI present but **no Docker daemon**, `curl` and
`sha256sum` present, arch x86_64. Alpine's LXC template has **no bash and no curl**
(busybox `ash` + `wget`), so every shell script shipped to users is POSIX `sh`.

---

## Decisions (locked by the interview)

| Area | Decision |
|---|---|
| Stack | Go, single static binary (`CGO_ENABLED=0`), web UI embedded via `embed.FS`. Zero third-party Go modules (`syscall.Statfs` for disk space). |
| Frontend | Vanilla HTML/CSS/JS, no build step, no third-party JS. Files: `web/index.html`, `web/app.js`, `web/style.css`. |
| Sharing model | One shared drop zone. File-manager view with breadcrumbs; one directory shown at a time; drops land in the directory being viewed. |
| Uploads | Chunked + resumable, tus-shaped protocol hand-rolled (not tusd: ~30 extra modules). 16 MiB chunks via `XMLHttpRequest` (fetch streaming needs HTTP/2). One chunk in flight per tab, files queued FIFO. Resume automatically within a page session; after a reload the user re-drops the same file and it continues from the server's offset (fingerprint in `localStorage`). |
| Folders | Drag-and-drop folders (desktop, `webkitGetAsEntry`) and a folder picker (`webkitdirectory`, works on iOS 18.4+, Android, desktop). Structure preserved. Folder delete and folder zip download. |
| Duplicates | Same name in same directory → auto-rename `name (1).ext`, `(2)`, … Never overwrite. |
| Snippets | Paste text anywhere on the page (outside inputs) → saved as a `.txt` item. Paste an image → uploaded as PNG. Visible "Add text" box for phones. Text items show a preview and a Copy button (`navigator.clipboard` if present, else `execCommand('copy')` fallback, because a LAN `http://` origin is not a secure context). |
| Live updates | Server-sent events. Other devices see new items, deletions, and in-progress uploads (name + %) without reloading. |
| Item metadata | Name, size, time ago, "expires in", device label derived server-side from User-Agent (e.g. "iPhone Safari", "Windows Chrome"). |
| Lifecycle | Every item expires `ttl` (default 168h) after upload completion (file mtime). No keep/pin. Sweeper every 15 min also removes partial uploads idle > 24h and empty directories. |
| Disk guard | Refuse an upload at init if `free - size < minFree` (default 1 GiB); re-check every 8 chunks. Optional `maxFileSize` (default 0 = unlimited). HTTP 507 / 413 with a clear message. |
| Access | No auth, no HTTPS, no IP filtering. Listens on `0.0.0.0:8080`. |
| Defaults | Binary/service/user `uped`; data `/var/lib/uped`; config `/etc/conf.d/uped`; logs `/var/log/uped/uped.log`. |
| Install | `install.sh` (POSIX sh, curl or wget) run as root inside an existing Alpine LXC: arch detect (amd64/arm64), download latest GitHub Release via `releases/latest/download/…` (no API, no jq), verify `checksums.txt`, create user, write OpenRC service (`supervise-daemon`), start, print `http://<lan-ip>:8080`. Re-run = upgrade. `--uninstall` keeps data; `--purge` removes data and user. |
| CI / release | GitHub Actions: vet + race tests + cross-build + Playwright on push/PR. On `v*` tags: `uped_linux_{amd64,arm64}.tar.gz` + `checksums.txt` to a GitHub Release, and a multi-arch image to `ghcr.io/stasnowak/uped`. |
| Tests | Go unit tests (names, store, sweeper, handlers) + Playwright end-to-end against the real binary. |
| Out of scope v1 | QR code on page (strong candidate for v1.1, see research), create/rename folders in UI, image previews, per-file share links, keep pin, Proxmox host helper script, apk package, HTTPS, auth, mDNS/avahi, PWA/share-sheet (needs HTTPS). |

### Known limitations to document (not bugs)
- iOS suspends page JavaScript a few seconds after Safari goes to background; uploads pause and resume when the tab is visible again. A locked phone does not keep uploading.
- After a page reload the browser cannot re-read a file; resume requires re-selecting the same file.
- Plain HTTP: browsers show "Not secure". Chrome's HTTPS-by-default exempts private IPs.

---

## Design reference

### Package layout
```
cmd/uped/                   config.go (flags + UPED_* env), main.go (logging, server start/shutdown, --version)
internal/names/             sanitise segments, dedupe "name (n).ext", device label from UA (pure, table-tested)
internal/store/             os.Root-based store: reserve, append, finish, abort, list, delete, zip, statfs, sweeper, meta.json
internal/server/            mux + handlers, SSE hub, download, zip, static
web/                        index.html, app.js, style.css; embed.go exports them as web.FS
e2e/                        Playwright project (package.json, playwright.config.ts, tests/)
install.sh                  user-facing installer (POSIX sh)
packaging/openrc/uped.initd template written by install.sh (also committed for review)
packaging/openrc/uped.confd
Dockerfile, Makefile, .github/workflows/{ci,release}.yml, README.md, CLAUDE.md
```

### Configuration (flag, env fallback, default)
| Flag | Env | Default |
|---|---|---|
| `--listen` | `UPED_LISTEN` | `:8080` |
| `--data` | `UPED_DATA_DIR` | `/var/lib/uped` (use `./data` in dev) |
| `--ttl` | `UPED_TTL` | `168h` (`0` disables expiry) |
| `--min-free` | `UPED_MIN_FREE` | `1G` |
| `--max-file-size` | `UPED_MAX_FILE_SIZE` | `0` (unlimited) |
| `--chunk-size` | `UPED_CHUNK_SIZE` | `16M` (advertised to clients) |
| `--version` | | prints version from `-ldflags -X main.version` |

### On-disk layout under `--data`
```
files/                 the shared tree, exactly as users see it (original names)
.parts/<id>.part       bytes received so far (opened O_APPEND)
.parts/<id>.json       {id,name,dir,size,fingerprint,device,created}  (offset = .part size; last activity = .part mtime)
meta.json              {"<relpath>": {"device": "...", "fingerprint": "..."}} written atomically, debounced to once per second
```
File mtime = time of last byte written (rename preserves it) = upload completion = TTL anchor.

### HTTP API
```
GET    /                          index.html (embedded)
GET    /static/{app.js,style.css} embedded assets, Cache-Control: no-cache
GET    /healthz                   "ok"
GET    /api/config                {version, chunkSize, ttlSeconds, maxFileSize, minFree, maxTextBytes}
GET    /api/list?path=<dir>       {path, entries:[{name,type,size,modified,expiresAt,device,preview?}], uploads:[{id,name,size,offset,device}], free}
GET    /api/events                SSE. starts with ": connected"; events: "change" {dir}, "upload" {id,dir,name,size,offset,state,path?}; ": ping" every 20 s
POST   /api/uploads               {name, dir, size, fingerprint} → 201 {id, offset, name, dir, state}; state "done" + path means already uploaded, skip; 413 too large; 507 disk
GET    /api/uploads/{id}          200 + Upload-Offset/Upload-Length headers + JSON state (HEAD: headers only), or 404
PUT    /api/uploads/{id}?offset=N body = one chunk (MaxBytesReader chunk+1) → 204 + Upload-Offset; 409 + Upload-Offset on mismatch; 507 disk
POST   /api/uploads/{id}/finish   fsync, re-dedupe target, rename into files/, write meta → 200 {path}
DELETE /api/uploads/{id}          abort, remove .part/.json
POST   /api/text                  {dir, text} (≤ 1 MiB) → 201 {path}; filename from first line slug or note-<timestamp>.txt
DELETE /api/items?path=<relpath>  file or directory (recursive)
GET    /d/<relpath>               download: ServeContent (Range, HEAD, If-*), hand-built Content-Disposition (ASCII fallback + filename*=UTF-8''), X-Content-Type-Options: nosniff
GET    /api/zip?path=<dir>        streamed zip (zip.Store, data descriptors, zip64 automatic), Flush after each file, name uped-<dir|all>-<YYYYMMDD-HHMM>.zip
```
Every user-supplied path: split on `/`, sanitise each segment (`internal/names`), join,
require `filepath.IsLocal`, and do all I/O through `os.OpenRoot(dataDir)` (Go 1.24:
Open/Create/OpenFile/Mkdir/Remove/Stat available; `Root.Rename` needs Go 1.25, so
rename with `os.Rename` on paths that already passed the Root/IsLocal checks).

`http.Server`: `ReadHeaderTimeout: 10s`, `IdleTimeout: 120s`, `ReadTimeout`/`WriteTimeout`
**0** (non-zero kills multi-GB uploads, zip streams and SSE); per-chunk deadline via
`http.NewResponseController(w).SetReadDeadline(now+5m)`.

### Upload client state machine (web/app.js)
1. Drop/pick/paste → build queue entries `{file, dir, fingerprint = name|size|lastModified|relPath}`.
2. For each entry (FIFO, one at a time): if `localStorage["uped.up." + fp]` holds an id → `HEAD /api/uploads/{id}`; 200 → continue from `Upload-Offset`; 404 → `POST /api/uploads`.
3. Loop: `xhr.open("PUT", /api/uploads/{id}?offset=off)`; `xhr.send(file.slice(off, off+chunk))`; `upload.onprogress` → bar = `(off + e.loaded) / size`. On 204 set `off` from header. On 409 set `off` from header and retry. On network error: retry after `[0,1,3,5,10]s`, then HEAD to resync and continue; after 5 consecutive failures mark "paused, tap to retry".
4. On `visibilitychange` → visible: HEAD + continue any paused entry.
5. `off == size` → `POST .../finish` → remove localStorage key → list refresh arrives via SSE.
6. Never use `FileReader`; never set `accept` on inputs (HEIC transcoding quirks).

---

## Phase 0: Repository scaffold
Status: Complete

- [x] `go mod init github.com/stasnowak/Uped` with `go 1.24` (matches this container and Alpine 3.22's `apk add go`); no third-party requires.
- [x] Create the package layout above with compiling stubs: `cmd/uped/main.go` (flag/env parsing, `--version`, starts server), empty `internal/{names,store,server}` packages, `web/index.html` placeholder.
- [x] `Makefile` targets: `build` (static, `-trimpath -ldflags "-s -w -X main.version=$(VERSION)"` into `dist/uped`), `test` (`go vet ./... && go test -race ./...`), `run` (`go run ./cmd/uped --data ./data --listen 127.0.0.1:8080`), `e2e`, `cross` (linux/amd64 + linux/arm64 tarballs + `checksums.txt` into `dist/`).
- [x] `.gitignore`: `dist/`, `data/`, `e2e/node_modules/`, `e2e/test-results/`, `e2e/playwright-report/`, `.e2e-data/`.
- [x] `.editorconfig` (tabs for Go, 2 spaces for JS/CSS/YAML).
- [x] `CLAUDE.md`: how to build/test/run, the no-deps rule, "scripts shipped to users are POSIX sh", "never run playwright install", pointer to this plan.
- [x] `README.md` stub with the one-paragraph pitch and a "work in progress" note (full docs in Phase 5).

### Verification Plan
- `go vet ./... && go test ./...` → exit 0 (no tests yet is fine).
- `make build && file dist/uped` → output contains `statically linked`.
- `dist/uped --version` → prints `dev` (or the injected version).
- `grep -c "require" go.mod` → `0`.

### Phase Summary
Completed 2026-10-07. All four verification checks passed:

| Check | Result |
|---|---|
| `go vet ./... && go test ./...` | exit 0; tests in `cmd/uped`, `internal/server`, `web` pass (also clean under `-race` and `gofmt -l`) |
| `make build && file dist/uped` | `ELF 64-bit LSB executable, x86-64 ... statically linked ... stripped`, about 6 MB |
| `dist/uped --version` | `uped 962cc57` from `make build` (git describe); `go run ./cmd/uped --version` prints `uped dev` |
| `grep -c "require" go.mod` | `0` |

Extra checks run: `make cross` produced `uped_linux_amd64.tar.gz`, `uped_linux_arm64.tar.gz` and
`checksums.txt` that pass `sha256sum -c`, and the arm64 binary is `ARM aarch64 ... statically linked`.
A live run served `/healthz` (200 `ok`), `/` (200 HTML), `HEAD /` (200), `/nope` (404), and exited 0 on SIGTERM.

What exists now:
- `go.mod`: module `github.com/stasnowak/Uped`, `go 1.24`, no requires.
- `cmd/uped/config.go`: `Config`, `parseConfig(args, getenv, out)`, size parser (`512K`, `16M`, `16MiB`, `1G`, `2T`; binary multiples; whole numbers only), TTL parser (Go durations plus `7d`), validation. Flags override `UPED_*` env vars. `--help` exits 0, bad flags or env exit 2 with a message. Fully table-tested in `config_test.go`.
- `cmd/uped/main.go`: `--version`, slog text logging to stderr, `os.MkdirAll(data, 0750)`, `http.Server` with the design-reference timeouts (`ReadHeaderTimeout` 10s, `IdleTimeout` 120s, Read/Write timeouts 0, 1 MiB headers), graceful shutdown on SIGINT/SIGTERM with a 10 s limit.
- `internal/server/server.go`: `New(Options{Version, Static})` serving `GET /{$}` (index.html, `no-cache`, `nosniff`) and `GET /healthz`. Tests use `fstest.MapFS`.
- `internal/names/doc.go`, `internal/store/doc.go`: package comments only.
- `web/embed.go` (`web.FS`, embeds `index.html`), `web/index.html` placeholder with `<title>uped</title>`, `web/embed_test.go`.
- `Makefile` (build, test, run, e2e, cross, clean), `.gitignore`, `.editorconfig`, `CLAUDE.md`, `README.md` stub.

Key decisions:
- **Embed location changed from the original plan.** `go:embed` cannot reference parent directories, so the embed lives in `web/embed.go` and `main` injects `web.FS` through `server.Options.Static`. Phase 2's static item is updated to match.
- **Phase 2 work done early:** server timeouts, graceful shutdown and `/healthz` already exist. Phase 2's items are annotated with what remains.
- `os.MkdirAll` in `run()` is a stand-in until `store.Open` takes over in Phase 2.
- `make e2e` exists but exits 1 with a message until Phase 4 creates `e2e/package.json`.
- `make cross` uses the exact release asset names, so Phase 5's `install.sh --binary` testing and Phase 6's release job can share them.

Next: Phase 1 starts in `internal/names` and `internal/store`. Remember the Go 1.24 limit: `os.Root` has Open/Create/OpenFile/Mkdir/Remove/Stat/Lstat but no `Rename`, `MkdirAll` or `RemoveAll` (those are 1.25).

## Phase 1: Store and naming (pure Go, unit-tested)
Status: Complete

- [x] `internal/names`: `Sanitize(segment string) string` — `path.Base`, strip chars `< 0x20`, `0x7f`, `/ \ : * ? " < > |`, trailing dots/spaces, Windows reserved stems (`CON PRN AUX NUL COM1-9 LPT1-9`), cap 255 bytes, empty → `unnamed`. `SplitRel(p string) ([]string, error)` for user-supplied relative paths (rejects empty result, `..`, absolute). Table tests including unicode (Polish names must survive unchanged), `../../etc/passwd`, `CON.txt`, 300-byte names.
- [x] `internal/names`: `Dedupe(exists func(string) bool, name string) string` → `name`, `name (1).ext`, `name (2).ext`… (extension-aware, `.tar.gz` treated as `.gz` is acceptable). Tests.
- [x] `internal/names`: `DeviceLabel(userAgent string) string` → "iPhone Safari", "iPad Safari", "Android Chrome", "Windows Chrome/Edge/Firefox", "Mac Safari/Chrome", "Linux Firefox", "ChromeOS Chrome", fallback "Unknown device". Table tests with real UA strings.
- [x] `internal/names`: `SnippetName(text string) string` → slug of first line (≤ 40 chars) + `.txt`, or `note-20061007-143012.txt`.
- [x] `internal/store`: `Open(dataDir string, opts Options) (*Store, error)` creating `files/`, `.parts/`, loading `meta.json` and reconciling (drop entries whose file is gone), rebuilding reservations from `.parts/*.json`.
- [x] `internal/store`: `Reserve(dir []string, name string, size int64, fingerprint, device string) (*Upload, error)` — mutex, `Dedupe` against `files/<dir>` + outstanding reservations, Statfs guard (`ErrInsufficientSpace`), max size (`ErrTooLarge`), creates `.part` with `O_CREATE|O_EXCL` and the `.json` sidecar.
- [x] `internal/store`: `Offset(id) (int64, error)`, `Append(id string, offset int64, r io.Reader, limit int64) (newOffset int64, err error)` — `Stat` size must equal `offset` else `ErrOffsetMismatch{Current}`; write with `O_APPEND`; update `lastActivity`; Statfs re-check every 8th append.
- [x] `internal/store`: `Finish(id) (relpath string, err error)` — require `offset == size`, fsync, re-run `Dedupe` (race), `MkdirAll` the target dir inside root, rename, write `meta.json` atomically, remove sidecar. `Abort(id)`.
- [x] `internal/store`: `List(dir []string) (Listing, error)` — entries newest first, `expiresAt = mtime + ttl`, device from meta, `preview` = first 200 chars for `.txt` ≤ 64 KiB; plus in-progress uploads targeting that dir; `free` bytes.
- [x] `internal/store`: `Delete(rel []string) error` (file or recursive dir, updates meta), `PutText(dir, text, device) (relpath, error)`.
- [x] `internal/store`: `OpenFile(rel) (*os.File, os.FileInfo, error)` for downloads; `WriteZip(w io.Writer, dir []string, flush func()) error` using `archive/zip` with `zip.Store`, `Modified` set, recursive, `flush()` after each file, stops cleanly on write error (client abort).
- [x] `internal/store`: `Sweep(now time.Time) (removedFiles, removedParts, removedDirs int)` — files with `mtime < now - ttl` (skip when ttl == 0), parts idle > 24h, empty dirs bottom-up (never `files/` itself). `RunSweeper(ctx, every 15m)`.
- [x] `internal/store`: change notifications — `Store.Events() <-chan Event` with `Event{Kind: change|upload, Dir, Upload *UploadInfo}` emitted on reserve/append (throttled to 1/s per upload)/finish/abort/delete/sweep.
- [x] Unit tests for all of the above against `t.TempDir()`: reserve→append (3 chunks)→finish round-trip with byte equality, offset mismatch returns current offset, duplicate names, finish race (same name reserved twice), delete dir, sweep TTL and stale parts and empty dirs, zip contents readable with `zip.NewReader` and names/sizes match, path escape attempts (`..`, symlink inside `files/` pointing outside) are rejected.

### Verification Plan
- `go vet ./... && go test -race -count=1 ./internal/...` → `ok` for `names` and `store`, zero failures.
- `go test -cover ./internal/names ./internal/store` → coverage ≥ 80% on both packages.
- `go test -run TestPathEscape -v ./internal/store` → prints the rejected cases and passes.

### Phase Summary
Completed 2026-10-07. Verification results:

| Check | Result |
|---|---|
| `go vet ./... && go test -race -count=1 ./internal/...` | all `ok`; also clean with `-count=5` and on the whole module |
| `go test -cover ./internal/names ./internal/store` | names 96.6%, store 84.3% |
| `go test -run TestPathEscape -v ./internal/store` | PASS, 35 rejected cases logged (symlink to outside, raw `..`, `a/b`, empty, `.`, NUL segments across List/Delete/OpenFile/Reserve/PutText/WriteZip) |

Extra: `FuzzSanitize` ran about 90 s in total. It found a real panic: `Dedupe` on a 255-byte name whose
"extension" was most of the name gave a negative slice bound, reachable by uploading such a name twice.
The fix treats extensions over 32 bytes as part of the stem. The crasher is kept in
`internal/names/testdata/fuzz/` as a permanent seed, and `TestDedupe` has a named regression case.
`GOOS=darwin` and `GOOS=windows` vet clean (fallback rename and free-space code compile).

**`internal/names` API** (differs from the plan's sketch in these ways):
- `SplitRel(p)` is for *lookups*: it validates without rewriting, rejecting any segment that `Sanitize` would change, so a request can never be silently mapped to a different file. `CleanRel(p)` is for *creation*: it sanitizes each segment. Both treat `/` and `\` as separators, ignore empty and `.` segments (so a leading `/` means the root rather than an error), reject `..`, NUL and invalid UTF-8, and cap depth at 64 and length at 3072 bytes. `Join(segs)` renders a path.
- `Sanitize` replaces `: * ? " < > |` with `_` (more readable than stripping; `14:30:12` becomes `14_30_12`), drops control, bidi-override and BOM characters, trims leading whitespace and trailing dots/whitespace, prefixes Windows device names with `_`, and caps at 255 bytes keeping an extension up to 32 bytes. Fuzzed property: idempotent, valid UTF-8, no separators, never `.`/`..`, always passes `SplitRel`.
- `Dedupe` replaces an existing ` (n)` suffix instead of stacking, keeps `.tar.gz`-style double extensions, and stays within 255 bytes.
- `SnippetName(text, now)` takes the clock as a parameter. A URL first line becomes `link-<host>.txt` (minus `www.`).
- `DeviceLabel` covers iPhone/iPad/Android/Windows/Mac/Linux/ChromeOS with Safari/Chrome/Firefox/Edge/Opera/Samsung Internet, plus `curl` and `wget`.

**`internal/store` behaviour worth knowing:**
- `Reserve(dir, name, size, fingerprint, device)` returns an `UploadInfo`, not a pointer. With a fingerprint it **resumes** an active upload with the same fingerprint, size and folder, and **skips** a finished file with the same fingerprint and size in that folder (`State == "done"`, `Path` set, no `ID`). Re-dropping a folder therefore continues unfinished files and does not duplicate finished ones.
- Name and folder matching is **case-insensitive**: `PHOTO.JPG` next to `photo.jpg` becomes `PHOTO (1).JPG`, and uploading into `trip/` merges into an existing `Trip/`. This prevents collisions when zips are extracted on Windows or macOS. A file in the way of a folder gives `ErrNotDir`.
- The disk guard counts bytes still owed to all active uploads. If the free-space probe fails, the guard is skipped with a warning rather than blocking uploads.
- The offset lives in memory (atomic) and is re-read from the part file after each write. The part file's mtime is the last-activity time, so the sidecar JSON is written once at reserve and has no `lastActivity` field.
- Renames use `renameat(2)` on directory handles opened through `os.Root` (`rename_linux.go`), so no path is resolved outside the root. Non-Linux falls back to `os.Rename` after root checks. `moveIntoLocked` retries once if the sweeper removed a just-emptied folder mid-finish.
- `meta.json` holds `{device, fingerprint}` per file and is written at most once per second (debounced), not once per finished file. `Close` and `FlushMeta` flush it. A corrupt file is logged and replaced.
- `Delete` of a folder aborts uploads heading into it. Deleting the root is refused.
- The sweeper removes an empty folder only if something inside it was removed in the same sweep, or it has been untouched for 10 minutes. This prevents a folder from vanishing while someone looks at it right after deleting its last file.
- `List` returns folders with recursive `items`, `size`, latest `modified` and `expiresAt`. It also returns active uploads into the folder *or any subfolder* (each with its `dir`), and `free` (-1 if unknown). `.txt` files of 64 KiB or less get a 200-character `preview`.
- `WriteZip` prefixes entries with the folder's own name (root zips have no prefix), includes empty folders, stores without compression, and skips symlinks. `StatDir` lets the handler fail before streaming starts.
- Events: `change` carries the folder whose contents changed; `upload` carries `UploadInfo` with state `active`/`done`/`aborted` (progress at most once per second per upload). Events are dropped, with a warning, if the 1024-slot buffer is full.

Next: Phase 2 wires these into HTTP handlers. The error-to-status mapping is listed in Phase 2.

## Phase 2: HTTP server
Status: Complete

- [x] `internal/server`: `New(store, cfg) http.Handler` using `http.NewServeMux` with Go 1.22 method patterns (`"PUT /api/uploads/{id}"`).
- [x] Static: add `app.js style.css` to the `//go:embed` line in `web/embed.go`; serve `/static/*` from `Options.Static` via `http.StripPrefix("/static/", http.FileServerFS(...))` with `Cache-Control: no-cache`. (`/` → `index.html` exists since Phase 0.)
- [x] `GET /api/config` (`GET /healthz` exists since Phase 0).
- [x] `GET /api/list` (400 on bad path, 404 on missing dir).
- [x] Uploads: `POST /api/uploads` (JSON ≤ 64 KiB via `MaxBytesReader`; device label from `User-Agent`), `HEAD /api/uploads/{id}`, `PUT /api/uploads/{id}?offset=N` (`MaxBytesReader(chunkSize+1)` → 413 on overflow; 409 with `Upload-Offset` on mismatch; 507 on `ErrInsufficientSpace`; per-request read deadline 5 min via `ResponseController`), `POST .../finish`, `DELETE /api/uploads/{id}`.
- [x] `POST /api/text` (≤ 1 MiB), `DELETE /api/items?path=`.
- [x] `GET /d/{path...}`: `http.ServeContent` on a store-opened file, `Content-Disposition: attachment; filename="<ascii>"; filename*=UTF-8''<escaped>` built by hand (test with `żółw 🐢.txt`), `nosniff`, `Content-Type` from extension else `application/octet-stream`.
- [x] `GET /api/zip?path=`: call `store.StatDir` first so a bad path gets a proper 404/400 before headers are sent; then `Content-Type: application/zip`, `Content-Disposition`, no `Content-Length`; `store.WriteZip` with `http.Flusher`; log and return on client abort.
- [x] SSE hub: `GET /api/events` sets `text/event-stream`, `Cache-Control: no-cache`, flushes a `: connected` comment immediately, fans out store events as `event: change` / `event: upload` with JSON data, `: ping` every 20 s, exits on `r.Context().Done()`. Slow clients get dropped (buffered channel, non-blocking send). Coalesce `change` events per folder (at most one per 500 ms) because each one makes every open page re-list, and `List` walks subfolders for their totals.
- [x] Error responses are JSON `{"error": "human readable"}` with the right status; the frontend shows them verbatim. Map store errors: `ErrNotFound` 404, `ErrBadPath` 400, `ErrInvalid` 400, `ErrIsDir` 400, `ErrNotDir` 409, `ErrIncomplete` 409, `*OffsetMismatchError` 409 + `Upload-Offset`, `ErrTooLarge` 413, `ErrChunkTooLarge` 413, `ErrInsufficientSpace` 507, anything else 500 (logged). Parse `dir`/`path` with `names.CleanRel` for uploads and text, `names.SplitRel` for everything else.
- [x] Request logging with `log/slog` (method, path, status, duration, client IP; one line per finished upload with path, size, device).
- [x] `cmd/uped/main.go`: config, server timeouts and graceful shutdown exist since Phase 0. Remaining: replace the `os.MkdirAll` stand-in with `store.Open`, pass the store into `server.New`, start the sweeper, log the reachable URL with the detected LAN IPv4 (for example `http://192.168.1.50:8080`), and make shutdown end SSE streams (`srv.RegisterOnShutdown` closing the hub) so it does not wait the full 10 s.
- [x] `internal/server` tests with `httptest`: full upload round-trip in 3 PUTs with byte-equal download; 409 path; 413 for oversized chunk; 507 when `minFree` is set above the temp dir's free space; Range request returns 206 with the right bytes; HEAD download; `Content-Disposition` for a non-ASCII name; zip response readable by `zip.NewReader`; SSE: connect, perform an upload, assert `change` and `upload` events arrive within 2 s; path traversal via URL (`/d/../../x`, `%2e%2e`) → 400/404, never a file outside `files/`.
- [x] `scripts/smoke.sh` (POSIX sh, curl): starts `dist/uped` on a temp dir and random port, exercises every endpoint, compares sha256 of a 40 MiB random file uploaded in 16 MiB chunks against the download, prints `SMOKE OK`.

### Verification Plan
- `go vet ./... && go test -race -count=1 ./...` → all packages `ok`.
- `make build && sh scripts/smoke.sh` → last line `SMOKE OK`.
- `go run ./cmd/uped --data ./data --listen 127.0.0.1:8080 &` then `curl -s localhost:8080/api/config` → JSON containing `"chunkSize":16777216`; `curl -sN localhost:8080/api/events | head -c 12` → `: connected`.

### Phase Summary
Completed 2026-10-07. Verification results:

| Check | Result |
|---|---|
| `go vet ./... && go test -race -count=1 ./...` | all five packages `ok`; server and store also pass `-count=5` under `-race` |
| `make build && sh scripts/smoke.sh` | `SMOKE OK` under `dash` (strict POSIX `/bin/sh` here): 25 checks including a 40 MiB random file in three 16 MiB chunks with matching sha256 |
| `go run ./cmd/uped --data ./data --listen 127.0.0.1:8080` | `/api/config` returned `"chunkSize":16777216`; `/api/events` first 12 bytes were `: connected` |

Extra checks: shutdown with an open event stream took 23 ms, and 1-2 ms with none (four timed runs). One
orphaned test process once took about 2 s to exit, which did not reproduce. `GOOS=darwin`, `windows` and
`linux/arm64` vet clean. With a wildcard listen address the log prints `open this on any device on your
network url=http://<lan-ip>:<port>/`.

What exists now:
- `internal/server`: `New(Options{Version, Static, Store, ChunkSize, Logger, IdleTimeout}) (*Server, error)`. `*Server` is the `http.Handler`; `Close()` ends live streams and is registered with `http.Server.RegisterOnShutdown`. Files: `server.go` (routes, static, config), `respond.go` (JSON and error mapping), `middleware.go` (logging, idle deadlines), `uploads.go`, `files.go`, `events.go`.
- Every route in the **HTTP API** reference, plus `GET /api/uploads/{id}` (JSON state; `HEAD` gives just the headers) and `GET /favicon.ico` (204).
- `cmd/uped/main.go` opens the store, builds the server, starts the 15-minute sweeper, and logs reachable URLs (`reachableURLs`, tested).
- `scripts/smoke.sh`: POSIX sh smoke test. It starts the binary with `--listen 127.0.0.1:0` and reads the chosen port from the log, so it needs no free-port tricks.
- `web/app.js` and `web/style.css` are placeholders, now embedded and linked from `index.html`.

Decisions and differences from the plan:
- **Idle deadlines instead of a fixed 5-minute chunk deadline.** Chunk bodies, downloads, zips and the event stream are cut after `IdleTimeout` (60 s) *without progress*, so a slow phone may take as long as it needs while a dead connection is dropped. net/http does not clear write deadlines between keep-alive requests when `Server.WriteTimeout` is 0, so every handler that sets one resets it on exit (`TestWriteDeadlineResetBetweenRequests` covers this with a raw keep-alive connection).
- **A failed zip stream aborts the connection** (`panic(http.ErrAbortHandler)`), so the browser reports a failed download rather than saving a truncated zip that looks complete.
- **Downloads are always attachments** and carry `Content-Security-Policy: sandbox` and `nosniff`, so an uploaded HTML file cannot run as a page on uped's origin. `Content-Disposition` is built by hand with an ASCII fallback (`download.ext` when nothing readable is ASCII) plus `filename*`.
- **Change-event throttling is leading-edge.** The first change goes out at once and later ones within 500 ms are merged into one batch per folder at the end of the window. Upload events are never delayed.
- Static assets are served from memory with content-hash ETags (`no-cache` plus revalidation, so a new build is picked up immediately).
- Logging: routine traffic (chunks, listings, static files, the stream) logs at Debug, which is hidden by default. Creates, finishes, downloads, zips, deletes log at Info; 4xx at Warn; 5xx at Error.
- An interrupted chunk answers 400 with `Upload-Offset` (bytes received so far are kept, see `TestInterruptedChunkKeepsBytes`). A disk-full write maps to 507.
- Path traversal over HTTP is refused everywhere (`TestPathTraversalViaHTTP`, 20 requests). The only non-4xx answer is Go's router redirecting `/d/../../etc/passwd` to `/etc/passwd`, which is a 404 on uped.

Notes for Phase 3 (also added to its items):
- `POST /api/uploads` answering `state: "done"` means skip the file. If `finish` returns 404 because its response was lost on a dropped connection, re-POST the create request: the fingerprint match answers `done` with the final path.
- An `upload` event with `state: "done"` can arrive up to 500 ms before the matching `change`. Treat it as a change of its `dir` too.
- Keep `<title>uped</title>`; the Go tests and the smoke script check for it.

## Phase 3: Web UI
Status: Not started

- [ ] `web/index.html`: header (app name, free space, TTL note), breadcrumb bar, action row (`Add files` `<input type=file multiple>`, `Add folder` `<input type=file webkitdirectory multiple>`, `Add text` toggles a textarea + Save), item list, upload queue panel, full-page drop overlay. Semantic HTML, `<meta name="viewport">`, no inline scripts.
- [ ] `web/style.css`: mobile-first single column, 44 px tap targets, `prefers-color-scheme` dark/light via CSS variables, drop overlay, progress bars, no horizontal scroll at 360 px.
- [ ] `web/app.js` state: `{dir, entries, uploads (server-side in progress), queue (local), config}`. Fetch `/api/config` then `/api/list?path=`; render list newest first: icon by type, name (click = download for files via a real `<a href="/d/...">`, enter for dirs), size, device, time ago, expires in; actions: Download, Copy (text items), Delete (confirm), Zip (dirs). Root "Download all as zip" button when the list is non-empty.
- [ ] Breadcrumbs reflect `dir`; browser `history.pushState` with `?path=` so back/forward and refresh keep the directory.
- [ ] Drag and drop: `dragenter/dragover/drop` on `document`, overlay while dragging, `DataTransferItem.webkitGetAsEntry()` recursion (`readEntries` until an empty batch) to collect `{file, relDir}`; fallback to `dataTransfer.files` when entries are unavailable.
- [ ] Pickers: files → `relDir = ""`; folder → `relDir = dirname(file.webkitRelativePath)`. Target directory for every upload = `join(currentDir, relDir)`.
- [ ] Upload engine exactly as in **Upload client state machine** (a `POST /api/uploads` answer with `state: "done"` means the server already has that file: mark it complete without sending bytes; if `finish` returns 404, re-POST the create request to learn whether it completed): `XMLHttpRequest` per chunk, `upload.onprogress`, retries, 409 resync, HEAD on `visibilitychange`, `localStorage` fingerprint map, one in flight, FIFO. Queue panel shows per-file bar, overall bar, speed and ETA (throttled 300 ms), Cancel (DELETE upload) and Retry.
- [ ] Error surfacing: 413 / 507 / network errors show the server's `error` text inline on the queue item; a 507 pauses the whole queue with a "Disk full on server" banner.
- [ ] Snippets: textarea Save → `POST /api/text`. Global `paste` listener (ignored when target is input/textarea): image items → upload as `pasted-<timestamp>.png`; plain text → `POST /api/text`. Text items render the `preview` and a Copy button that fetches `/d/<path>` then copies via `navigator.clipboard?.writeText` or the hidden-textarea `execCommand('copy')` fallback; show "Copied" toast.
- [ ] Live updates: `EventSource('/api/events')`; on `change` whose `dir` is the current folder, inside it, or above it → refetch the list (debounced ~300 ms), and treat an `upload` event with `state: "done"` the same way for its `dir`; on `upload` → upsert a greyed "incoming" row with name and % for uploads targeting the current dir (skip ones that are in this tab's own queue); close on `pagehide`; on `error` the browser reconnects, refetch the list on `open`.
- [ ] Empty state copy ("Drop files here or tap Add files. Everything disappears after 7 days.") and a footer line with version.
- [ ] Manual check in the real browser via the `run` skill: desktop drop of a folder, phone-width viewport, dark mode screenshot.

### Verification Plan
- `go build ./... && go test ./internal/server` → still green (static assets embedded).
- `go run ./cmd/uped --data ./.e2e-data --listen 127.0.0.1:18080 &` then a headless Chromium script via Playwright (`cd e2e && npx playwright test tests/smoke.spec.ts` once Phase 4 exists, otherwise a one-off `node` script using `playwright`): page title is `uped`, no console errors, `setInputFiles` on `input[type=file]` with two files → two rows appear within 5 s.
- `node -e "require('fs').readFileSync('web/app.js','utf8')" && node --check web/app.js` → exit 0 (syntax).

### Phase Summary
_(write when phase completes)_

## Phase 4: End-to-end tests (Playwright)
Status: Not started

- [ ] `e2e/package.json` with `@playwright/test` pinned to `1.56.1` (matches `/opt/pw-browsers/chromium-1194`); `e2e/playwright.config.ts` with `webServer: { command: "go run ../cmd/uped --listen 127.0.0.1:18080 --data ./.e2e-data --ttl 1h --chunk-size 4M", url: "http://127.0.0.1:18080/healthz", reuseExistingServer: false }`, `globalSetup` wipes `.e2e-data`, projects: `chromium-desktop` and `chromium-mobile` (iPhone 13 viewport, `hasTouch`). Document `PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers` and never `playwright install` locally; CI does `npx playwright install --with-deps chromium`.
- [ ] `tests/helpers.ts`: generate fixture files of given size with deterministic content, sha256 helper, `dropFiles(page, files)` that dispatches a synthetic `drop` with a `DataTransfer`, `apiList(request, dir)`.
- [ ] `basic.spec.ts`: empty state renders; pick two small files → rows with correct names/sizes; download via `request.get('/d/<name>')` is byte-equal; delete removes the row; breadcrumb navigation into a folder and back.
- [ ] `dragdrop.spec.ts` (desktop only): synthetic drop of two files lands in the current dir; drop into a subfolder view lands there.
- [ ] `folder.spec.ts`: `setInputFiles` on the `webkitdirectory` input with a fixture directory (nested 2 levels) → tree preserved on server (`apiList` of subdirs); folder zip via `request.get('/api/zip?path=...')` returns `application/zip` with the `PK` magic and non-trivial size; folder delete removes everything.
- [ ] `resume.spec.ts`: 20 MiB fixture with 4 MiB chunks: (a) `page.route` aborts the 2nd `PUT` once → upload still completes, server sha256 matches; (b) route aborts every `PUT` after the 1st, wait for "paused" state, `page.reload()`, unroute, `setInputFiles` the same file → observe a `HEAD /api/uploads/<id>` followed by a `PUT` with `offset=4194304`, final file byte-equal; (c) `DELETE` on cancel removes the `.part`.
- [ ] `sse.spec.ts`: two browser contexts; upload in A → row appears in B without reload within 5 s; B sees the greyed incoming row with a percentage while a throttled (`page.route` delay) upload runs in A; delete in A disappears in B.
- [ ] `text.spec.ts`: Add text → `.txt` row with preview; synthetic `paste` event with `text/plain` → new row; Copy button click does not throw and the toast appears; synthetic paste with an image `File` → `pasted-*.png` row.
- [ ] `limits.spec.ts`: start a second server instance in the test with `--max-file-size 1M` (spawn `go run` on another port) → picking a 2 MiB file shows the 413 message inline; `--min-free` set to an absurd value → 507 banner.
- [ ] `mobile.spec.ts` (mobile project): no horizontal overflow (`document.scrollingElement.scrollWidth <= innerWidth`), buttons visible above the fold, picker upload works.
- [ ] `make e2e` target: exists since Phase 0 (guarded until `e2e/package.json` exists); confirm it runs the suite.

### Verification Plan
- `cd e2e && npm ci && PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers npx playwright test` → all specs pass, output ends with `N passed`.
- `npx playwright test --project=chromium-mobile` → passes.
- Run the suite twice in a row (`--repeat-each 2`) → no flaky failures.

### Phase Summary
_(write when phase completes)_

## Phase 5: Installer, OpenRC service, documentation
Status: Not started

- [ ] `packaging/openrc/uped.initd` (template, `#!/sbin/openrc-run`): `supervisor=supervise-daemon`, `command=/usr/local/bin/uped`, `command_args` built from `/etc/conf.d/uped` variables (`--listen "$UPED_LISTEN" --data "$UPED_DATA_DIR" $UPED_EXTRA_ARGS`), `command_user="uped:uped"`, `directory`, `pidfile=/run/uped.pid`, `output_log`/`error_log=/var/log/uped/uped.log`, `respawn_delay=5`, `respawn_max=0`, `umask=027`, `depend() { need net; after firewall; use dns logger; }`, `start_pre()` with `checkpath -d -m 0750 -o uped:uped` for data and log dirs.
- [ ] `packaging/openrc/uped.confd`: `UPED_LISTEN=":8080"`, `UPED_DATA_DIR="/var/lib/uped"`, `UPED_TTL="168h"`, `UPED_MIN_FREE="1G"`, `UPED_MAX_FILE_SIZE="0"`, `UPED_EXTRA_ARGS=""`, each commented. (The init script passes these as flags; conf.d vars are not exported.)
- [ ] `install.sh` (POSIX sh, `set -eu`, everything in `main()` invoked on the last line so a truncated download cannot run half a script): root check; Alpine + OpenRC check (`/etc/alpine-release`, `command -v rc-service`) with a clear message otherwise; arch map (`x86_64|amd64→amd64`, `aarch64|arm64→arm64`); `dl()` using curl else busybox wget; `UPED_VERSION` override else `releases/latest/download/`; download `uped_linux_<arch>.tar.gz` + `checksums.txt` to `mktemp -d`, `sha256sum -c`; extract; compare hash with existing binary → "already up to date" skips restart; else `rc-service uped stop` (ignore failure), `chmod 755`, `mv -f` into `/usr/local/bin/uped`; `addgroup -S uped; adduser -S -D -H -h /var/lib/uped -s /sbin/nologin -G uped uped`; create `/var/lib/uped` and `/var/log/uped` (chown top-level only, no `-R`); always (re)write `/etc/init.d/uped`; write `/etc/conf.d/uped` only if absent; `rc-update add uped default`; `rc-service uped start` (or `restart`); banner with `http://<ip>:8080` where ip = `ip -4 route get 1 | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}'` (fallback `hostname -i`).
- [ ] `install.sh` flags: `--uninstall` (stop, `rc-update del`, remove init script and binary, keep conf and data), `--purge` (also remove `/etc/conf.d/uped`, `/var/lib/uped`, `/var/log/uped`, `deluser uped`), `--version vX.Y.Z`, and for testing: `--binary PATH` (skip download), `--prefix DIR` (install under DIR, implies `--no-service`), `--no-service` (skip rc-update/rc-service). Usage text on `--help`. Usable as `curl -fsSL URL | sh -s -- --uninstall`.
- [ ] `scripts/test-install.sh`: builds `dist/uped`, runs `install.sh --binary dist/uped --prefix "$tmp"`, asserts the binary, init script, conf.d exist with the right modes, `sh -n "$tmp/etc/init.d/uped"` passes, re-run prints "already up to date", `--uninstall --prefix` removes binary and init script but keeps conf.d, `--purge` removes everything. Runs without root and without OpenRC.
- [ ] `README.md`: pitch; screenshot placeholder; **Quick start on Proxmox** (`pveam update`, download the latest `alpine-*` template, `pct create` example with 1 vCPU / 512 MB / 2 GB rootfs, unprivileged, DHCP or static IP, optional `pct set <id> -mp0 local-lvm:50,mp=/var/lib/uped` for a bigger data volume), the one-liner for both curl and wget (`wget -qO- URL | sh` since the Alpine template lacks curl), what gets installed where, configuration table, upgrade (re-run), uninstall/purge, logs (`tail -f /var/log/uped/uped.log`, `rc-service uped status`), Docker usage (Phase 6), how uploads/expiry work, limitations (HTTP only, iOS background, resume needs re-drop), security note (no auth: LAN only, never port-forward), building from source (`apk add go git && go build ./cmd/uped`), license.
- [ ] `docs/CONFIG.md` only if README grows past ~250 lines; otherwise keep it in README.

### Verification Plan
- `sh -n install.sh && sh -n packaging/openrc/uped.initd` → exit 0.
- `sh scripts/test-install.sh` → last line `INSTALL TEST OK`.
- `shellcheck -s sh install.sh` → no errors. shellcheck is not installed in this container (checked 2026-10-07); if it still is not, note that in the summary and rely on the CI job, which should run it via `ludeeus/action-shellcheck` or `apt-get install shellcheck`.
- `grep -n 'bash' install.sh` → no matches (POSIX only).

### Phase Summary
_(write when phase completes)_

## Phase 6: CI, releases, Docker image
Status: Not started

- [ ] `.github/workflows/ci.yml` on push + PR: `actions/checkout`, `actions/setup-go` (`go-version-file: go.mod`, cache), `go vet ./...`, `go test -race ./...`, `sh scripts/test-install.sh`, cross-build matrix `GOOS=linux GOARCH={amd64,arm64} CGO_ENABLED=0`, then an `e2e` job: `actions/setup-node` (22), `cd e2e && npm ci && npx playwright install --with-deps chromium && npx playwright test`, upload `playwright-report` on failure. Use the current major versions of each action at implementation time (research found checkout v7, setup-go v7, action-gh-release v3, docker login/buildx v4, build-push v7 as of Oct 2026; verify on the Marketplace).
- [ ] `.github/workflows/release.yml` on `push: tags: ['v*']`, `permissions: {contents: write, packages: write}`: build both arches with `-X main.version=${GITHUB_REF_NAME}`, `tar czf uped_linux_<arch>.tar.gz uped`, `sha256sum *.tar.gz > checksums.txt`, `softprops/action-gh-release` with `files`, `generate_release_notes: true`, `fail_on_unmatched_files: true`.
- [ ] `Dockerfile`: `FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build` (or newer), `ARG TARGETOS TARGETARCH`, `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/uped ./cmd/uped`; `FROM scratch`, `COPY --from=build /out/uped /uped`, `USER 65532:65532`, `VOLUME /data`, `EXPOSE 8080`, `ENTRYPOINT ["/uped","--data","/data","--listen",":8080"]`. Add `.dockerignore`.
- [ ] Release workflow image job: `docker/login-action` to `ghcr.io` with `GITHUB_TOKEN`, `docker/metadata-action` (`ghcr.io/stasnowak/uped`, tags `semver {{version}}`, `{{major}}.{{minor}}`, `latest`), `docker/setup-buildx-action`, `docker/build-push-action` with `platforms: linux/amd64,linux/arm64`, `push: true`, `cache-from/to: type=gha`. No QEMU needed (Go cross-compiles).
- [ ] README: Docker section (`docker run -d -p 8080:8080 -v uped-data:/data ghcr.io/stasnowak/uped`), note that the image runs as uid 65532 so a bind-mounted host dir must be writable by it.
- [ ] `Makefile cross` target produces the same artifact names as the release job so `install.sh --binary` can be tested against them.

### Verification Plan
- `make cross && ls dist/` → `uped_linux_amd64.tar.gz uped_linux_arm64.tar.gz checksums.txt`; `cd dist && sha256sum -c checksums.txt` → both `OK`.
- `tar xzf dist/uped_linux_arm64.tar.gz -O uped | file -` → `ARM aarch64 ... statically linked`.
- Workflow files parse: `python3 -I -c "import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]" .github/workflows/*.yml` → exit 0 (PyYAML is present in this container).
- Docker build cannot run here (no daemon): verify `Dockerfile` by review and by the first tagged release's CI run (Phase 7).
- After the first push to GitHub: CI run on the branch is green (check via the GitHub MCP `actions_list`/`actions_get` tools).

### Phase Summary
_(write when phase completes)_

## Phase 7: First release and real-world check
Status: Not started

- [ ] Final pass: `go vet`, `go test -race`, e2e, `scripts/smoke.sh`, `scripts/test-install.sh` all green on the branch; README reviewed end to end; `CLAUDE.md` current.
- [ ] Bump nothing (version comes from the tag). Confirm the GitHub repo name/case used in URLs (`stasnowak/Uped` for raw/install URLs; `ghcr.io/stasnowak/uped` lowercase for the image).
- [ ] Open a PR from `claude/home-file-transfer-app-9os7eu` to `main` **only when the user asks**; merging and tagging `v0.1.0` are the user's actions (tag push triggers release.yml).
- [ ] After `v0.1.0` exists: verify the release has 3 assets and the ghcr.io package is public; run `install.sh` with no flags in a throwaway Alpine LXC on the user's Proxmox (user-run; cannot be automated from this environment) and confirm: service starts, URL printed is reachable from a phone, a 1 GB upload from a phone completes, resume after toggling Wi-Fi works, expiry sweeper logs a run.
- [ ] Record any field issues as checkboxes in a new **Phase 8: Field fixes** and address them.

### Verification Plan
- GitHub MCP: `list_releases` for `stasnowak/Uped` shows `v0.1.0` with `uped_linux_amd64.tar.gz`, `uped_linux_arm64.tar.gz`, `checksums.txt`.
- `curl -fsSL https://github.com/stasnowak/Uped/releases/latest/download/checksums.txt` → three lines.
- User confirms the LXC checklist above (manual).

### Phase Summary
_(write when phase completes)_

## Final Recap
_(write when all phases complete: summary of the entire piece of work)_

## Deployment Plan
_(write when all phases complete: step-by-step deployment instructions)_
