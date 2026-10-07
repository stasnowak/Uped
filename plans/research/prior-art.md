# Uped prior-art survey (2026-10-07)

Stars are live GitHub counts as of today. "Single bin" = one static executable with embedded UI.

## Core projects

| Project | Lang | Single bin | Upload mechanism | No-auth mode | Deploy | Stars | UI model |
|---|---|---|---|---|---|---|---|
| [PairDrop](https://github.com/schlagmichdoch/PairDrop) (fork of [Snapdrop](https://github.com/SnapDrop/snapdrop), 19.7k, dormant; snapdrop.net sold to LimeWire) | JS/Node | No | WebRTC P2P (WS signalling); server stores nothing, both devices must be online | Yes (always) | Docker / Node | 11.5k | Device discovery + pairing; send files/text; PWA, iOS Share sheet |
| [dufs](https://github.com/sigoden/dufs) | Rust | Yes | Browser: one XHR `PUT` per file, **sequential** (`DUFS_MAX_UPLOADINGS=1`); failed upload has a Retry button that resumes via `HEAD` + `PATCH X-Update-Range: append`; folders via `webkitGetAsEntry` | Read-only by default; `-A` / `--allow-upload` enables writes, optional `-a user:pass` | Binary, cargo, Docker, brew | 10.8k | Shared folder listing (sort/search), `?zip` folder download, WebDAV |
| [copyparty](https://github.com/9001/copyparty) | Python | Single-file `copyparty-sfx.py` (needs Python); also Docker | **up2k**: client-side hashing, parallel chunks, resumes across browser restarts, dedupe, `--u2sz` chunk size; folder drag; paste/CTRL-V; plain multipart fallback | No args = rw on cwd; volumes `-v path:url:perms` | sfx / pip / Docker / exe | 46.9k | Dense file-manager + uploader tabs; `?zip/?tar`; `lifetime=N` volflag auto-deletes (needs `-e2d`); "unpost" undo; QR; share links; media player |
| [miniserve](https://github.com/svenstaro/miniserve) | Rust | Yes | Plain `multipart/form-data` (`curl -F`), no chunk/resume; upload only with `-u`; `-U` mkdir, `-R` delete | Yes (default); optional `-a` | Binary, cargo, Docker, distro pkgs | 7.9k | Shared folder listing; `-r/-g/-z` tar/zip on the fly; `-q` QR in terminal |
| [File Browser](https://github.com/filebrowser/filebrowser) | Go | Yes | **tus** chunked/resumable since v2.24 (pause/resume, progress); folder upload | `filebrowser config set --auth.method=noauth` | Binary, Docker | 35.9k | Full file manager + share links w/ expiry. **Archived Sep 2026**, "treat as unmaintained"; fork [FileBrowser Quantum](https://github.com/gtsteffaniak/filebrowser) 8.5k active |
| [PsiTransfer](https://github.com/psi-4ward/psitransfer) | JS/Node + Vue | No | **tus** resumable up/down | Yes unless `PSITRANSFER_UPLOAD_PASS` set | Docker / Node | 2.0k | WeTransfer-style: upload -> bucket link (expiry, one-time, password); zip/tar.gz of bucket (not resumable); 109 open issues |
| [Gokapi](https://github.com/Forceu/Gokapi) | Go | Yes | Chunked since v1.6 (CVE-2026-30961: chunk size-limit bypass, fixed 2.2.4) | **No** — admin login to upload; "file request" links let outsiders upload | Binary, Docker | 2.9k | Share links expiring by days/downloads; S3, E2E |
| [PicoShare](https://github.com/mtlynch/picoshare) | Go | Yes (SQLite stores blobs) | Single POST, no chunking ([#477](https://github.com/mtlynch/picoshare/issues/477) open) | **No** — `PS_SHARED_SECRET`; "guest links" allow unauth upload | Binary, Docker, fly.io | 3.0k | Share links with per-file expiry; no folders |
| [Pingvin Share](https://github.com/stonith404/pingvin-share) | TS (NestJS/Next) | No | Chunked | No (accounts; admin) | Docker | 4.7k | Share links, reverse shares, expiry. **Archived May 2026** -> fork Pingvin Share X |
| [Send](https://github.com/timvisee/send) (Firefox Send fork, primary on [GitLab](https://gitlab.com/timvisee/send)) | JS/Node | No (needs Redis for prod) | Browser-side E2E encryption, streamed over WebSocket | Yes (optional) | Docker | 3.8k | One-shot share links with time/download expiry |
| [uploadserver](https://github.com/Densaugeo/uploadserver) | Python | pip module (`python3 -m uploadserver`) | Plain multipart POST to `/upload`; streams to disk since 4.0; no folders | Yes; optional `--basic-auth[-upload]` | pip | 360 | http.server dir listing + an upload form; no TTL |

## Other notable projects

| Project | Notes |
|---|---|
| [LocalSend](https://github.com/localsend/localsend) (93.5k, Dart) | Native app, not web; what r/selfhosted recommends over PairDrop when 8 GB transfers fail. Needs app on every device. |
| [DumbDrop](https://github.com/DumbWareio/DumbDrop) (571, Node) | Closest "dumb drop page": drag-drop + folder upload (keeps structure), optional PIN, Docker; no chunking, **no expiry**. |
| [transfer.sh](https://github.com/dutchcoders/transfer.sh) (15.9k, Go) | curl-first upload -> link, default 14-day TTL; minimal web UI. |
| [qrcp](https://github.com/claudiodangelis/qrcp) (10.4k, Go CLI) | One-shot: prints QR, phone opens upload/download page. Proves QR-to-URL is the phone handoff that works. |
| [croc](https://github.com/schollz/croc) (40.5k, Go CLI) | CLI relay transfer; not browser. |
| [LANServer](https://github.com/Vigneshkumar212/LANServer) (0, Node) / featherdrop (Docker, tus+SQLite, expiry, E2E, link model, GitHub not found) / [ShareDrop](https://github.com/ShareDropio/sharedrop) (10.8k, bought by LimeWire) / [Sharry](https://github.com/eikek/sharry) (1.3k Scala) / Erugo (Laravel) / Jirafeau (PHP) / Lufi (Perl) | Same two camps: WebRTC device-to-device, or upload -> share link. Nobody ships "shared LAN feed + TTL" as a single Go binary. |

## Synthesis

**1. UX details the best-liked tools get right**
- copyparty up2k: visible upload queue with per-file state, parallel chunks, survives tab close/browser restart, dedupes re-drops, "unpost" undo for your own uploads, CTRL-V paste of files/text, QR code for the current URL, uploader IP + time shown in the list.
- dufs: one uncluttered page, drag a folder in and it recurses, per-file progress with speed and ETA (throttled 300 ms), Retry button that actually resumes, `?zip` on any folder, sortable list + search.
- PairDrop: zero decisions (see device, tap, done), text messages, overall progress for multi-file sends, iOS Share-sheet integration via PWA; reverted in-browser zipping to save memory.
- miniserve/qrcp: QR code of the URL is the real phone-onboarding mechanism; keep it on-page.
- PsiTransfer/featherdrop/Picoshare: expiry picker at upload time, one-time links. Mobile layouts that work are the ones that are a single column list with big tap targets (PairDrop, copyparty's mobile mode).

**2. Recurring complaints in issue trackers**
- Large files: dufs [#257](https://github.com/sigoden/dufs/issues/257) 7.5 GB dies at 3.5 GB (closed, not planned); miniserve [#1541](https://github.com/svenstaro/miniserve/issues/1541) >2 GB stuck at 0 % (regression tied to `crypto.subtle`), [#1368](https://github.com/svenstaro/miniserve/issues/1368) no size limit; PicoShare [#595](https://github.com/mtlynch/picoshare/issues/595) 504 on 1.6 GB, [#122](https://github.com/mtlynch/picoshare/issues/122) >5 GB; PairDrop [#120](https://github.com/schlagmichdoch/PairDrop/issues/120) >500 MB disconnects (open, most-upvoted); Gokapi [#81](https://github.com/Forceu/Gokapi/issues/81) timeouts. Root cause is almost always one giant request hitting a proxy/body/timeout limit or a 2/4 GB boundary.
- iOS Safari: PairDrop [#128](https://github.com/schlagmichdoch/PairDrop/issues/128) reconnect loop, [#336](https://github.com/schlagmichdoch/PairDrop/issues/336) gallery share broken, [#425](https://github.com/schlagmichdoch/PairDrop/issues/425); copyparty [#791](https://github.com/9001/copyparty/issues/791) no folder picker on iOS, [#1069](https://github.com/9001/copyparty/issues/1069) Shortcut uploads only first image. Platform facts: iOS has no directory picker; FileReader on large blobs can crash the tab (WebKit bug 160650); background tab = upload paused.
- HTTP-only: `crypto.subtle`, Clipboard API, PWA install and `share_target` need a secure context. PairDrop effectively requires HTTPS; miniserve's hashing regression hit http users. A LAN tool on plain http must not depend on these.
- Disk full: copyparty [#587](https://github.com/9001/copyparty/issues/587) negative free-space calc; nobody else pre-checks space. PicoShare needs manual `VACUUM` after deletes.
- No cleanup: dufs, miniserve, DumbDrop, uploadserver have no TTL at all; copyparty's `lifetime` needs the indexing flag; users bolt on cron.
- Folder upload: dufs [#94](https://github.com/sigoden/dufs/issues/94) some files in a folder fail with no retry-all; copyparty docs: Firefox crashes after ~4000 files, Chrome skips symlinks; Gokapi [#106](https://github.com/Forceu/Gokapi/issues/106) wants folder upload.
- Zip download: dufs [#79](https://github.com/sigoden/dufs/issues/79) corrupt zips, [#318](https://github.com/sigoden/dufs/issues/318) cancelling a zip download broke the server, [#555](https://github.com/sigoden/dufs/issues/555) streamed zip flagged as "zip bomb"; copyparty [#1443](https://github.com/9001/copyparty/issues/1443) archives truncated; Pingvin crashed on >4 GB zips; PsiTransfer [#244](https://github.com/psi-4ward/psitransfer/issues/244) one-time + zip. Lesson: zip64, store (no deflate), data descriptors, and handle client abort.

**3. Features a minimal clone should not skip**
- Chunked + resumable upload with automatic retry, a visible queue (2-3 concurrent), per-file and overall progress with speed/ETA, and a server-side max size with a clear error.
- Folder drag-drop via `webkitGetAsEntry` plus a file-picker fallback (iOS only ever gets files).
- Text/clipboard snippets as first-class items (PairDrop, copyparty both have it; users ask for it).
- Download-all / download-selection as streamed zip64; single-file direct links.
- QR code of the server URL on the page; list showing name, size, age, "expires in", source device/IP; newest first.
- Delete (at least your own uploads) and free-space indicator; pre-check space before accepting a chunk.
- Live list updates (nothing in this set does SSE cleanly; copyparty polls) and full function on plain http.

**4. Is Uped redundant?**
- dufs: `dufs -A /srv/drop` + a cron `find -mtime +7 -delete` covers ~70 % of Uped in one line. Gaps: one file at a time, whole-file PUT (resume only after manual Retry), no TTL, no live list, no text snippets, no QR, and it is a filesystem view, not a drop feed.
- copyparty: functionally already does all of it — `copyparty -v /srv/drop::rw:c,lifetime=604800` gives resumable uploads, text paste, QR, zip, expiry. For a solo power user Uped is redundant against this. Its costs: Python runtime instead of one static binary, a ~200 KB README, volflag configuration, and a UI that even fans call "obnoxious"/confusing at first; client-side hashing before upload is slow on phones and Firefox (copyparty itself says Chrome is up to 90 % faster at hashing).
- PairDrop is a different category (no store-and-forward: both devices must be open at once), File Browser is archived, PsiTransfer/Gokapi/PicoShare/Send are share-link tools for strangers, not a household feed.
- Uped's defensible value: zero-config single static Go binary (`./uped /srv/drop`, nothing else) for an Alpine LXC; TTL on by default with the countdown visible; a feed-style UI designed for "drop here, grab there" rather than a file manager; SSE so the other device sees the file appear; resumable chunks without a pre-hash step so phones start uploading instantly; everything working on `http://10.0.0.x:8080` with no secure-context APIs. If it is not clearly simpler than copyparty and more robust at uploads than dufs, it has no reason to exist.
