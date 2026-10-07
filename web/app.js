// uped browser UI: plain JavaScript, no build step, no dependencies.
//
// The page is served over plain HTTP on a LAN address, which browsers do not
// treat as a secure context. So: no service workers, no navigator.clipboard
// (copying falls back to execCommand), no crypto.subtle. Uploads use
// XMLHttpRequest because streaming fetch uploads need HTTP/2 and only
// XHR reports upload progress.
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const RETRY_DELAYS = [0, 1000, 3000, 5000, 10000];
  const MAX_QUEUE_ROWS = 100;
  const LS_PREFIX = "uped.up.";
  const LS_MAX_AGE = 2 * 24 * 3600 * 1000;

  const state = {
    config: { chunkSize: 16 << 20, ttlSeconds: 7 * 24 * 3600, version: "", maxTextBytes: 1 << 20 },
    dir: [], // current folder as path segments
    listing: null, // last /api/list answer for state.dir
    incoming: new Map(), // id -> upload from another device heading into this folder
    queue: [], // this tab's uploads
    running: false,
    diskFull: false,
  };
  const ownIds = new Set(); // server ids of this tab's uploads, to skip their live events

  // ---------- small helpers

  const joinPath = (segs) => segs.filter(Boolean).join("/");
  const splitPath = (p) => (p || "").split("/").filter((s) => s && s !== ".");
  const dirname = (p) => splitPath(p).slice(0, -1).join("/");
  const encodeRel = (rel) => splitPath(rel).map(encodeURIComponent).join("/");
  const fileURL = (rel) => "/d/" + encodeRel(rel);
  const zipURL = (rel) => "/api/zip" + (rel ? "?path=" + encodeURIComponent(rel) : "");
  const pageURL = (segs) => (segs.length ? "/?path=" + encodeURIComponent(joinPath(segs)) : "/");
  const plural = (n, word) => n + " " + word + (n === 1 ? "" : "s");

  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === "class") el.className = v;
      else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? "" : String(v));
    }
    for (const c of children) if (c != null && c !== false) el.append(c);
    return el;
  }

  const SVG_NS = "http://www.w3.org/2000/svg";
  const ICONS = {
    folder: ["M3 7.5A2.5 2.5 0 0 1 5.5 5H9l2 2.2h7.5A2.5 2.5 0 0 1 21 9.7v7.8a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 17.5z"],
    file: ["M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z", "M14 3v5h5"],
    text: ["M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z", "M14 3v5h5", "M8.5 13h7", "M8.5 16.5h5"],
    image: ["M5 4h14a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z", "M4 16l4.5-4.5 4 4 2.5-2.5 5 5", "M10.5 8.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 1 1 3 0"],
    video: ["M4 6h11a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1z", "M16 10.5l5-3v9l-5-3"],
    audio: ["M9 18V5.5l11-2V16", "M9 18a3 3 0 1 1-6 0 3 3 0 1 1 6 0", "M20 16a3 3 0 1 1-6 0 3 3 0 1 1 6 0"],
    archive: ["M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z", "M14 3v5h5", "M10 5h1.5M10 8h1.5M10 11h1.5", "M9.5 14h3v3h-3z"],
    pdf: ["M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z", "M14 3v5h5", "M8.5 16.5c2-1 4.5-4.5 4-7-.6-2.6-2.7 4.7 3 6"],
    upload: ["M12 19V7", "M6.5 12.5 12 7l5.5 5.5", "M5 4h14"],
    download: ["M12 4v12", "M6.5 10.5 12 16l5.5-5.5", "M5 20h14"],
    zip: ["M12 4v12", "M6.5 10.5 12 16l5.5-5.5", "M5 20h14"],
    trash: ["M4 7h16", "M9.5 7V4.5h5V7", "M6 7l1 13h10l1-13", "M10 11v5.5M14 11v5.5"],
    copy: ["M9 9h11v11H9z", "M5 15H4V4h11v1"],
    close: ["M6 6l12 12", "M18 6 6 18"],
    retry: ["M20 11a8 8 0 1 0-2.3 5.7", "M20 4v7h-7"],
  };
  function icon(name) {
    const svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("aria-hidden", "true");
    for (const d of ICONS[name] || ICONS.file) {
      const p = document.createElementNS(SVG_NS, "path");
      p.setAttribute("d", d);
      svg.append(p);
    }
    return svg;
  }

  const EXT_KIND = {};
  for (const [kind, exts] of Object.entries({
    image: "jpg jpeg png gif webp heic heif avif bmp svg tif tiff raw dng",
    video: "mp4 mov m4v mkv webm avi 3gp wmv mts",
    audio: "mp3 m4a aac flac wav ogg opus wma",
    text: "txt md csv log json xml yaml yml ini",
    archive: "zip 7z rar tar gz tgz xz bz2 zst iso dmg",
    pdf: "pdf",
  })) for (const e of exts.split(" ")) EXT_KIND[e] = kind;
  const kindOf = (name) => EXT_KIND[(name.split(".").pop() || "").toLowerCase()] || "file";

  function fmtSize(n) {
    if (!(n >= 0)) return "";
    if (n < 1024) return n + " B";
    const units = ["KB", "MB", "GB", "TB"];
    let v = n / 1024;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return v.toFixed(v >= 100 ? 0 : 1) + " " + units[i];
  }

  function fmtSpan(secs) {
    secs = Math.max(0, Math.round(secs));
    if (secs < 60) return secs + " s";
    const m = Math.round(secs / 60);
    if (m < 60) return m + " min";
    const hrs = Math.floor(m / 60);
    return hrs + " h" + (m % 60 ? " " + (m % 60) + " min" : "");
  }

  function timeAgo(iso) {
    const s = (Date.now() - new Date(iso).getTime()) / 1000;
    if (!(s >= 0) || s < 45) return "just now";
    if (s < 3600) return plural(Math.max(1, Math.round(s / 60)), "min") + " ago";
    if (s < 86400) return Math.round(s / 3600) + " h ago";
    return plural(Math.round(s / 86400), "day") + " ago";
  }

  function expiresIn(iso) {
    if (!iso) return "";
    const s = (new Date(iso).getTime() - Date.now()) / 1000;
    if (s <= 60) return "expires now";
    if (s < 3600) return "expires in " + Math.round(s / 60) + " min";
    if (s < 48 * 3600) return "expires in " + Math.round(s / 3600) + " h";
    return "expires in " + Math.round(s / 86400) + " days";
  }

  function ttlText(secs) {
    if (!secs) return "";
    if (secs % 86400 === 0) return plural(secs / 86400, "day");
    if (secs % 3600 === 0) return plural(secs / 3600, "hour");
    return fmtSpan(secs);
  }

  function stamp(d = new Date()) {
    const p = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
  }

  let toastTimer = 0;
  function toast(msg, isError) {
    const t = $("toast");
    t.textContent = msg;
    t.classList.toggle("error", !!isError);
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (t.hidden = true), isError ? 6000 : 2500);
  }

  // ---------- server calls

  class ApiError extends Error {
    constructor(status, message) {
      super(message);
      this.status = status;
    }
  }

  async function api(method, url, body) {
    const opts = { method, headers: {}, cache: "no-store" };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    let res;
    try {
      res = await fetch(url, opts);
    } catch {
      throw new ApiError(0, "The server is unreachable");
    }
    let data = null;
    if ((res.headers.get("Content-Type") || "").includes("application/json")) {
      try {
        data = await res.json();
      } catch {
        data = null;
      }
    }
    if (!res.ok) throw new ApiError(res.status, (data && data.error) || "HTTP " + res.status);
    return data;
  }

  // headOffset resolves to the server's byte count for an upload, null when
  // the upload no longer exists, and rejects on network failure.
  async function headOffset(id) {
    const r = await fetch("/api/uploads/" + encodeURIComponent(id), { method: "HEAD", cache: "no-store" });
    if (r.status === 404) return null;
    if (!r.ok) throw new Error("HTTP " + r.status);
    return Number(r.headers.get("Upload-Offset")) || 0;
  }

  // ---------- resume bookkeeping (best effort: storage can be unavailable)

  function savedUpload(fp) {
    try {
      const v = JSON.parse(localStorage.getItem(LS_PREFIX + fp) || "null");
      return v && v.id ? v.id : null;
    } catch {
      return null;
    }
  }
  function saveUpload(fp, id) {
    try {
      localStorage.setItem(LS_PREFIX + fp, JSON.stringify({ id, t: Date.now() }));
    } catch {
      /* ignore */
    }
  }
  function forgetUpload(fp) {
    try {
      localStorage.removeItem(LS_PREFIX + fp);
    } catch {
      /* ignore */
    }
  }
  function pruneSaved() {
    try {
      for (let i = localStorage.length - 1; i >= 0; i--) {
        const k = localStorage.key(i);
        if (!k || !k.startsWith(LS_PREFIX)) continue;
        const v = JSON.parse(localStorage.getItem(k) || "null");
        if (!v || !(Date.now() - v.t < LS_MAX_AGE)) localStorage.removeItem(k);
      }
    } catch {
      /* ignore */
    }
  }

  // ---------- navigation and listing

  function currentFromURL() {
    return splitPath(new URLSearchParams(location.search).get("path"));
  }

  function navigate(segs, how = "push") {
    state.dir = segs;
    if (how === "push") history.pushState(null, "", pageURL(segs));
    if (how === "replace") history.replaceState(null, "", pageURL(segs));
    state.listing = null;
    state.incoming.clear();
    render();
    loadList();
  }

  let listSeq = 0;
  let listTimer = 0;
  function scheduleList(delay = 300) {
    clearTimeout(listTimer);
    listTimer = setTimeout(loadList, delay);
  }

  async function loadList() {
    clearTimeout(listTimer);
    const seq = ++listSeq;
    const dir = joinPath(state.dir);
    try {
      const data = await api("GET", "/api/list?path=" + encodeURIComponent(dir));
      if (seq !== listSeq) return;
      state.listing = data;
      state.incoming = new Map(data.uploads.filter((u) => !ownIds.has(u.id)).map((u) => [u.id, u]));
      render();
    } catch (e) {
      if (seq !== listSeq) return;
      if ((e.status === 404 || e.status === 409 || e.status === 400) && state.dir.length) {
        toast("That folder is gone. It may have expired or been deleted.", true);
        navigate(state.dir.slice(0, -1), "replace");
        return;
      }
      toast("Could not load the list: " + e.message, true);
    }
  }

  // ---------- rendering

  function render() {
    renderCrumbs();
    renderHeader();
    renderList();
  }

  function renderCrumbs() {
    const nav = $("crumbs");
    nav.replaceChildren();
    const parts = [["Home", []]].concat(state.dir.map((s, i) => [s, state.dir.slice(0, i + 1)]));
    parts.forEach(([label, segs], i) => {
      if (i) nav.append(h("span", { class: "sep", "aria-hidden": "true" }, "/"));
      if (i === parts.length - 1) {
        nav.append(h("span", { "aria-current": "page" }, label));
      } else {
        nav.append(h("a", { href: pageURL(segs), onclick: (e) => { e.preventDefault(); navigate(segs); } }, label));
      }
    });
    $("drop-target").textContent = state.dir.length ? state.dir[state.dir.length - 1] : "Home";
  }

  function renderHeader() {
    const free = state.listing ? state.listing.free : -1;
    $("free").hidden = !(free >= 0);
    $("free").textContent = free >= 0 ? fmtSize(free) + " free" : "";
    const zip = $("zip-all");
    const hasEntries = !!(state.listing && state.listing.entries.length);
    zip.hidden = !hasEntries;
    zip.href = zipURL(joinPath(state.dir));
    zip.textContent = state.dir.length ? "Download folder" : "Download all";
  }

  let listFrame = 0;
  function renderListSoon() {
    if (!listFrame) listFrame = requestAnimationFrame(() => { listFrame = 0; renderList(); });
  }

  function renderList() {
    const ul = $("list");
    const cur = joinPath(state.dir);
    const rows = [];
    const incoming = [...state.incoming.values()].sort((a, b) => String(a.created).localeCompare(String(b.created)));
    for (const u of incoming) rows.push(incomingRow(u, cur));
    const entries = state.listing ? state.listing.entries : [];
    for (const e of entries) rows.push(e.type === "dir" ? dirRow(e) : fileRow(e));
    ul.replaceChildren(...rows);
    $("empty").hidden = !state.listing || rows.length > 0;
  }

  const NBSP = String.fromCharCode(160);
  // Each part ("expires in 7 days") wraps as a unit; lines break only at
  // the separators.
  function metaLine(parts) {
    return h("div", { class: "meta" }, parts.filter(Boolean).map((p) => p.split(" ").join(NBSP)).join(" · "));
  }

  function fileRow(e) {
    const rel = joinPath([...state.dir, e.name]);
    const kind = kindOf(e.name);
    const isText = typeof e.preview === "string" && e.preview !== "";
    return h("li", { class: "item file", "data-name": e.name },
      h("span", { class: "ficon" }, icon(kind)),
      h("a", { class: "name", href: fileURL(rel), download: e.name }, e.name),
      metaLine([fmtSize(e.size), e.device, timeAgo(e.modified), expiresIn(e.expiresAt)]),
      isText ? h("div", { class: "preview" }, e.preview) : null,
      h("div", { class: "ibtns" },
        isText ? h("button", { type: "button", class: "icon-btn", title: "Copy text", "aria-label": "Copy text of " + e.name, onclick: () => copyEntry(e, rel) }, icon("copy")) : null,
        h("a", { class: "icon-btn", href: fileURL(rel), download: e.name, title: "Download", "aria-label": "Download " + e.name }, icon("download")),
        h("button", { type: "button", class: "icon-btn danger", title: "Delete", "aria-label": "Delete " + e.name, onclick: (ev) => deleteItem(ev, rel, e.name, false) }, icon("trash"))));
  }

  function dirRow(e) {
    const segs = [...state.dir, e.name];
    const rel = joinPath(segs);
    const open = (ev) => { ev.preventDefault(); navigate(segs); };
    const summary = e.items ? plural(e.items, "file") + " · " + fmtSize(e.size) : "empty folder";
    return h("li", { class: "item dir", "data-name": e.name },
      h("span", { class: "ficon" }, icon("folder")),
      h("a", { class: "name", href: pageURL(segs), onclick: open }, e.name),
      metaLine([summary, timeAgo(e.modified), expiresIn(e.expiresAt)]),
      h("div", { class: "ibtns" },
        e.items ? h("a", { class: "icon-btn", href: zipURL(rel), title: "Download as zip", "aria-label": "Download " + e.name + " as zip" }, icon("zip")) : null,
        h("button", { type: "button", class: "icon-btn danger", title: "Delete folder", "aria-label": "Delete folder " + e.name, onclick: (ev) => deleteItem(ev, rel, e.name, true) }, icon("trash"))));
  }

  function incomingRow(u, cur) {
    const where = u.dir === cur ? "" : u.dir.slice(cur ? cur.length + 1 : 0) + "/";
    const pct = u.size ? Math.floor((u.offset / u.size) * 100) : 0;
    const bar = h("div", { class: "bar", role: "progressbar", "aria-valuenow": pct, "aria-valuemin": 0, "aria-valuemax": 100 }, h("span"));
    bar.firstChild.style.width = pct + "%";
    return h("li", { class: "item incoming", "data-name": where + u.name },
      h("span", { class: "ficon" }, icon("upload")),
      h("span", { class: "name" }, where + u.name),
      metaLine(["Incoming" + (u.device ? " from " + u.device : ""), fmtSize(u.size), pct + "%"]),
      bar);
  }

  // ---------- item actions

  async function deleteItem(ev, rel, name, isDir) {
    const msg = isDir ? `Delete the folder "${name}" and everything in it?` : `Delete "${name}"?`;
    if (!confirm(msg + "\nThis removes it for every device.")) return;
    const li = ev.currentTarget.closest("li");
    if (li) li.classList.add("pending");
    try {
      await api("DELETE", "/api/items?path=" + encodeURIComponent(rel));
      toast("Deleted " + name);
    } catch (e) {
      toast("Could not delete: " + e.message, true);
    }
    loadList();
  }

  async function copyEntry(e, rel) {
    let text = e.preview;
    // The preview is the whole file when the byte counts match; copying it
    // directly keeps the click's user activation, which Safari needs.
    if (new TextEncoder().encode(text).length !== e.size) {
      try {
        const r = await fetch(fileURL(rel), { cache: "no-store" });
        if (!r.ok) throw new Error("HTTP " + r.status);
        text = await r.text();
      } catch (err) {
        toast("Could not fetch the text: " + err.message, true);
        return;
      }
    }
    copyText(text);
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(() => toast("Copied"), () => copyFallback(text));
      return;
    }
    copyFallback(text);
  }

  function copyFallback(text) {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("aria-hidden", "true");
    Object.assign(ta.style, { position: "fixed", top: "0", left: "0", width: "1px", height: "1px", opacity: "0", fontSize: "16px" });
    document.body.append(ta);
    ta.focus();
    ta.select();
    ta.setSelectionRange(0, text.length);
    let ok = false;
    try {
      ok = document.execCommand("copy");
    } catch {
      ok = false;
    }
    ta.remove();
    if (ok) {
      toast("Copied");
      return;
    }
    $("copy-text").value = text;
    $("copy-dialog").showModal();
    $("copy-text").select();
  }

  // ---------- text snippets

  async function postText(text) {
    if (!text.trim()) {
      toast("Nothing to save", true);
      return false;
    }
    if (new TextEncoder().encode(text).length > state.config.maxTextBytes) {
      toast("That text is too long to save as a note; save it as a file instead.", true);
      return false;
    }
    try {
      const data = await api("POST", "/api/text", { dir: joinPath(state.dir), text });
      toast("Saved " + splitPath(data.path).pop());
      scheduleList(0);
      return true;
    } catch (e) {
      toast("Could not save the text: " + e.message, true);
      return false;
    }
  }

  function toggleComposer(open) {
    const c = $("composer");
    c.hidden = !open;
    $("text-toggle").setAttribute("aria-expanded", String(open));
    if (open) $("text-input").focus();
  }

  async function saveComposer() {
    const ta = $("text-input");
    $("text-save").disabled = true;
    const ok = await postText(ta.value);
    $("text-save").disabled = false;
    if (ok) {
      ta.value = "";
      toggleComposer(false);
    }
  }

  // ---------- picking files: buttons, drag and drop, paste

  function fromFileList(files, useRelative) {
    return [...files].map((file) => ({ file, relDir: useRelative ? dirname(file.webkitRelativePath || "") : "" }));
  }

  function readEntries(reader) {
    return new Promise((resolve, reject) => reader.readEntries(resolve, reject));
  }

  async function walkEntry(entry, prefix, out) {
    if (entry.isFile) {
      try {
        const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
        out.push({ file, relDir: prefix });
      } catch {
        /* unreadable file: skip */
      }
      return;
    }
    if (!entry.isDirectory) return;
    const dir = prefix ? prefix + "/" + entry.name : entry.name;
    const reader = entry.createReader();
    for (;;) {
      let batch;
      try {
        batch = await readEntries(reader); // Chrome returns at most 100 per call
      } catch {
        return;
      }
      if (!batch.length) return;
      for (const child of batch) await walkEntry(child, dir, out);
    }
  }

  // collectDrop must read the DataTransfer synchronously: its items are
  // emptied as soon as the drop event handler returns.
  function collectDrop(dt) {
    const entries = [];
    const loose = [];
    for (const it of dt.items || []) {
      if (it.kind !== "file") continue;
      const entry = typeof it.webkitGetAsEntry === "function" ? it.webkitGetAsEntry() : null;
      if (entry) {
        entries.push(entry);
      } else {
        const f = it.getAsFile();
        if (f) loose.push({ file: f, relDir: "" });
      }
    }
    if (!entries.length && !loose.length) return Promise.resolve(fromFileList(dt.files || [], false));
    return (async () => {
      for (const entry of entries) await walkEntry(entry, "", loose);
      return loose;
    })();
  }

  const hasFiles = (e) => !!e.dataTransfer && [...(e.dataTransfer.types || [])].includes("Files");
  let dragDepth = 0;
  function showDrop(on) {
    $("drop").hidden = !on;
  }

  function renamePasted(file) {
    if (file.name && !/^image\.\w+$/i.test(file.name)) return file;
    const ext = (file.type.split("/")[1] || "bin").replace("jpeg", "jpg").replace(/[^a-z0-9]/gi, "");
    return new File([file], `pasted-${stamp()}.${ext}`, { type: file.type, lastModified: Date.now() });
  }

  function onPaste(e) {
    const t = e.target;
    if (t && t.closest && t.closest("input, textarea, [contenteditable]")) return;
    const dt = e.clipboardData;
    if (!dt) return;
    const files = [];
    for (const it of dt.items || []) {
      if (it.kind === "file") {
        const f = it.getAsFile();
        if (f) files.push(renamePasted(f));
      }
    }
    if (files.length) {
      e.preventDefault();
      enqueue(files.map((file) => ({ file, relDir: "" })));
      return;
    }
    const text = dt.getData("text/plain");
    if (text && text.trim()) {
      e.preventDefault();
      postText(text);
    }
  }

  // ---------- upload queue

  let nextKey = 1;
  function enqueue(picked) {
    if (!picked.length) return;
    const base = joinPath(state.dir);
    for (const { file, relDir } of picked) {
      const dir = joinPath([base, relDir]);
      state.queue.push({
        key: nextKey++,
        file,
        name: file.name || "unnamed",
        size: file.size,
        dir,
        fp: [file.name, file.size, file.lastModified, dir].join("|"),
        id: null,
        offset: 0,
        sent: 0,
        state: "queued",
        error: "",
        failures: 0,
        path: "",
        samples: [],
        xhr: null,
        cancelled: false,
      });
    }
    renderQueue();
    pump();
  }

  async function pump() {
    if (state.running || state.diskFull) return;
    const item = state.queue.find((i) => i.state === "queued");
    if (!item) {
      renderQueue();
      return;
    }
    state.running = true;
    try {
      await runItem(item);
    } catch (e) {
      if (!item.cancelled) fail(item, e.message || String(e));
    } finally {
      state.running = false;
      if (item.state === "uploading") item.state = item.cancelled ? "cancelled" : "queued";
      renderQueue();
      pump();
    }
  }

  // Wake every pending retry early, e.g. when a phone comes back to the tab.
  const wakers = new Set();
  function sleep(ms) {
    return new Promise((resolve) => {
      const done = () => {
        clearTimeout(timer);
        wakers.delete(done);
        resolve();
      };
      const timer = setTimeout(done, ms);
      wakers.add(done);
    });
  }
  function wakeAll() {
    for (const w of [...wakers]) w();
  }

  async function runItem(item) {
    item.state = "uploading";
    item.error = "";
    renderQueue();

    if (!item.id) {
      const saved = savedUpload(item.fp);
      if (saved) {
        const off = await headOffset(saved).catch(() => undefined);
        if (typeof off === "number") {
          item.id = saved;
          item.offset = item.sent = off;
          ownIds.add(saved);
        } else if (off === null) {
          forgetUpload(item.fp);
        }
      }
    }
    if (!item.id && !(await create(item))) return;

    for (;;) {
      if (!(await sendChunks(item))) return;
      const next = await finish(item);
      if (next !== "again") return;
    }
  }

  // create reserves the upload. False means stop: the server already had
  // the file, or it failed. sentAll marks a re-check after a lost finish
  // answer, where "done" means this tab's own upload completed.
  async function create(item, sentAll) {
    for (;;) {
      if (item.cancelled) return false;
      try {
        const data = await api("POST", "/api/uploads", { name: item.name, dir: item.dir, size: item.size, fingerprint: item.fp });
        if (data.state === "done") {
          complete(item, data.path, !sentAll);
          return false;
        }
        item.id = data.id;
        item.offset = item.sent = data.offset || 0;
        ownIds.add(item.id);
        saveUpload(item.fp, item.id);
        return true;
      } catch (e) {
        if (e.status === 507) return diskFull(item, e.message);
        if (e.status && e.status < 500) return fail(item, e.message);
        if (!(await backoff(item, e.message))) return false;
      }
    }
  }

  // sendChunks sends the rest of the file. True means every byte is on the
  // server.
  async function sendChunks(item) {
    while (item.offset < item.size) {
      if (item.cancelled) return false;
      const end = Math.min(item.offset + state.config.chunkSize, item.size);
      const r = await putChunk(item, item.offset, end);
      if (item.cancelled) return false;
      if (r.status === 204) {
        item.offset = item.sent = r.offset != null ? r.offset : end;
        item.failures = 0;
        continue;
      }
      if (r.status === 409 && r.offset != null) {
        item.offset = item.sent = r.offset;
        continue;
      }
      if (r.status === 404) return gone(item);
      if (r.status === 507) return diskFull(item, r.error);
      if (r.status === 413) return fail(item, r.error);
      // Network failure, interrupted body (400) or server error: keep what
      // the server has, wait, resynchronise and carry on.
      if (r.offset != null) item.offset = item.sent = r.offset;
      if (!(await backoff(item, r.error))) return false;
      const off = await headOffset(item.id).catch(() => undefined);
      if (off === null) return gone(item);
      if (typeof off === "number") item.offset = item.sent = off;
    }
    return true;
  }

  function putChunk(item, start, end) {
    return new Promise((resolve) => {
      const x = new XMLHttpRequest();
      item.xhr = x;
      x.open("PUT", "/api/uploads/" + encodeURIComponent(item.id) + "?offset=" + start);
      x.setRequestHeader("Content-Type", "application/offset+octet-stream");
      x.upload.onprogress = (e) => {
        item.sent = start + e.loaded;
        sample(item);
        scheduleQueueRender();
      };
      const settle = (status) => {
        item.xhr = null;
        let error = "";
        try {
          error = JSON.parse(x.responseText).error || "";
        } catch {
          error = "";
        }
        const off = x.getResponseHeader && status ? x.getResponseHeader("Upload-Offset") : null;
        resolve({ status, offset: off != null && off !== "" ? Number(off) : null, error: error || (status ? "HTTP " + status : "Connection lost") });
      };
      x.onload = () => settle(x.status);
      x.onerror = x.onabort = x.ontimeout = () => settle(0);
      x.send(item.file.slice(start, end)); // a lazy slice: nothing is read into memory here
    });
  }

  async function finish(item) {
    try {
      const data = await api("POST", "/api/uploads/" + encodeURIComponent(item.id) + "/finish");
      complete(item, data.path, false);
      return "done";
    } catch (e) {
      if (item.cancelled) return "stop";
      if (e.status === 409) {
        const off = await headOffset(item.id).catch(() => undefined);
        if (typeof off === "number") item.offset = item.sent = off;
        return "again";
      }
      if (e.status === 404) {
        // Most likely the finish worked but its answer was lost. Asking to
        // create the same file again tells us: "done" if it is there.
        forgetUpload(item.fp);
        ownIds.delete(item.id);
        item.id = null;
        item.offset = item.sent = 0;
        return (await create(item, true)) ? "again" : "stop";
      }
      if (e.status === 507) {
        diskFull(item, e.message);
        return "stop";
      }
      if (e.status && e.status < 500) {
        fail(item, e.message);
        return "stop";
      }
      return (await backoff(item, e.message)) ? "again" : "stop";
    }
  }

  async function backoff(item, why) {
    item.failures++;
    if (item.failures > RETRY_DELAYS.length) {
      item.state = "paused";
      item.error = (why || "Connection lost") + ". Tap Retry to continue.";
      renderQueue();
      return false;
    }
    item.error = (why || "Connection lost") + ", retrying…";
    renderQueue();
    await sleep(RETRY_DELAYS[item.failures - 1]);
    item.error = "";
    return !item.cancelled;
  }

  function sample(item) {
    const now = performance.now();
    item.samples.push([now, item.sent]);
    while (item.samples.length > 2 && now - item.samples[0][0] > 5000) item.samples.shift();
  }

  function speedOf(item) {
    const s = item.samples;
    if (s.length < 2) return 0;
    const [t0, b0] = s[0];
    const [t1, b1] = s[s.length - 1];
    return t1 > t0 ? ((b1 - b0) / (t1 - t0)) * 1000 : 0;
  }

  function complete(item, path, skipped) {
    item.state = skipped ? "skipped" : "done";
    item.path = path || "";
    item.offset = item.sent = item.size;
    item.error = "";
    forgetUpload(item.fp);
    if (item.id) ownIds.delete(item.id);
    if (affects(dirname(item.path))) scheduleList(150);
    renderQueue();
  }

  function fail(item, msg) {
    item.state = "error";
    item.error = msg || "Upload failed";
    renderQueue();
    return false;
  }

  function gone(item) {
    forgetUpload(item.fp);
    if (item.id) ownIds.delete(item.id);
    item.id = null;
    item.offset = item.sent = 0;
    return fail(item, "The server dropped this upload (its folder may have been deleted). Tap Retry to start it again.");
  }

  function diskFull(item, msg) {
    item.state = "paused";
    item.diskFull = true;
    item.error = msg || "Not enough free disk space on the server";
    state.diskFull = true;
    const b = $("banner");
    b.textContent = "Disk full on the server: uploads are paused. Delete something here to make room, then tap Retry.";
    b.hidden = false;
    renderQueue();
    return false;
  }

  function cancelItem(item) {
    item.cancelled = true;
    if (item.xhr) item.xhr.abort();
    if (item.id) {
      api("DELETE", "/api/uploads/" + encodeURIComponent(item.id)).catch(() => {});
      ownIds.delete(item.id);
    }
    forgetUpload(item.fp);
    item.state = "cancelled";
    item.error = "";
    wakeAll();
    renderQueue();
  }

  function retryItem(item) {
    if (item.diskFull || state.diskFull) {
      state.diskFull = false;
      $("banner").hidden = true;
      for (const i of state.queue) {
        if (i.diskFull && i.state === "paused") {
          i.diskFull = false;
          i.state = "queued";
          i.failures = 0;
          i.error = "";
        }
      }
    }
    item.state = "queued";
    item.failures = 0;
    item.error = "";
    item.cancelled = false;
    renderQueue();
    pump();
  }

  function resumePaused() {
    wakeAll();
    let any = false;
    for (const i of state.queue) {
      if (i.state === "paused" && !i.diskFull) {
        i.state = "queued";
        i.failures = 0;
        i.error = "";
        any = true;
      }
    }
    if (any) {
      renderQueue();
      pump();
    }
  }

  // ---------- queue rendering

  let queueTimer = 0;
  function scheduleQueueRender() {
    if (!queueTimer) queueTimer = setTimeout(renderQueue, 300);
  }

  const FINISHED = new Set(["done", "skipped", "cancelled"]);
  const ORDER = { uploading: 0, paused: 1, error: 1, queued: 2, done: 3, skipped: 3, cancelled: 4 };

  function renderQueue() {
    clearTimeout(queueTimer);
    queueTimer = 0;
    const q = state.queue;
    $("queue").hidden = q.length === 0;
    updateTitle();
    if (!q.length) return;

    const live = q.filter((i) => i.state !== "cancelled");
    const total = live.reduce((n, i) => n + i.size, 0);
    const sent = live.reduce((n, i) => n + Math.min(i.sent, i.size), 0);
    const pct = total ? Math.floor((sent / total) * 100) : 100;
    const finished = live.filter((i) => i.state === "done" || i.state === "skipped").length;
    const active = q.find((i) => i.state === "uploading");
    const stuck = q.filter((i) => i.state === "error" || i.state === "paused").length;

    let summary;
    if (active) {
      const speed = speedOf(active);
      const remaining = total - sent;
      summary = `Uploading ${Math.min(finished + 1, live.length)} of ${live.length} · ${pct}%` +
        (speed > 0 ? ` · ${fmtSize(speed)}/s · ${fmtSpan(remaining / speed)} left` : "");
    } else if (q.some((i) => i.state === "queued")) {
      summary = `Waiting · ${pct}%`;
    } else {
      summary = plural(finished, "file") + " uploaded" + (stuck ? ` · ${stuck} need attention` : "");
    }
    $("queue-summary").textContent = summary;
    const bar = $("queue-bar");
    bar.firstChild.style.width = pct + "%";
    bar.setAttribute("aria-valuenow", pct);
    bar.classList.toggle("done", !active && !stuck && pct === 100);
    $("queue-clear").hidden = !q.some((i) => FINISHED.has(i.state));

    const sorted = [...q].sort((a, b) => ORDER[a.state] - ORDER[b.state] || a.key - b.key);
    const rows = sorted.slice(0, MAX_QUEUE_ROWS).map(queueRow);
    if (sorted.length > MAX_QUEUE_ROWS) rows.push(h("li", { class: "queue-more" }, `and ${sorted.length - MAX_QUEUE_ROWS} more`));
    $("queue-list").replaceChildren(...rows);
  }

  function queueRow(item) {
    const pct = item.size ? Math.floor((Math.min(item.sent, item.size) / item.size) * 100) : 100;
    let status;
    let cls = "qstatus";
    switch (item.state) {
      case "uploading": {
        const speed = speedOf(item);
        status = item.error || `${pct}%` + (speed > 0 ? ` · ${fmtSize(speed)}/s · ${fmtSpan((item.size - item.sent) / speed)} left` : "");
        break;
      }
      case "queued":
        status = "Queued · " + fmtSize(item.size);
        break;
      case "done":
        status = "Uploaded · " + fmtSize(item.size);
        cls += " done";
        break;
      case "skipped":
        status = "Already on the server";
        cls += " done";
        break;
      case "cancelled":
        status = "Cancelled";
        break;
      default:
        status = item.error;
        cls += " error";
    }
    let bar = null;
    if (!FINISHED.has(item.state)) {
      bar = h("div", { class: "bar" + (item.state === "error" ? " error" : "") }, h("span"));
      bar.firstChild.style.width = pct + "%";
    }
    const buttons = [];
    if (item.state === "error" || item.state === "paused") {
      buttons.push(h("button", { type: "button", class: "icon-btn", title: "Retry", "aria-label": "Retry " + item.name, onclick: () => retryItem(item) }, icon("retry")));
    }
    if (!FINISHED.has(item.state)) {
      buttons.push(h("button", { type: "button", class: "icon-btn danger", title: "Cancel", "aria-label": "Cancel " + item.name, onclick: () => cancelItem(item) }, icon("close")));
    }
    const shownName = item.path ? splitPath(item.path).pop() : item.name;
    const where = item.dir && item.dir !== joinPath(state.dir) ? item.dir + "/" : "";
    return h("li", { class: "qitem", "data-state": item.state },
      h("div", { class: "qname" }, where + shownName),
      h("div", { class: "qbtns" }, ...buttons),
      h("div", { class: cls }, status),
      bar);
  }

  function updateTitle() {
    const live = state.queue.filter((i) => i.state !== "cancelled");
    const busy = live.some((i) => i.state === "uploading" || i.state === "queued");
    if (!busy) {
      document.title = "uped";
      return;
    }
    const total = live.reduce((n, i) => n + i.size, 0);
    const sent = live.reduce((n, i) => n + Math.min(i.sent, i.size), 0);
    document.title = (total ? Math.floor((sent / total) * 100) : 0) + "% · uped";
  }

  // ---------- live updates

  function affects(dir) {
    const cur = joinPath(state.dir);
    return cur === "" || dir === "" || dir === cur || dir.startsWith(cur + "/") || cur.startsWith(dir + "/");
  }

  function parse(data) {
    try {
      return JSON.parse(data);
    } catch {
      return null;
    }
  }

  function onUploadEvent(u) {
    if (!u || (u.id && ownIds.has(u.id))) return;
    const cur = joinPath(state.dir);
    if (u.state === "active") {
      if (cur === "" || u.dir === cur || u.dir.startsWith(cur + "/")) {
        state.incoming.set(u.id, u);
        renderListSoon();
      }
      return;
    }
    if (state.incoming.delete(u.id)) renderListSoon();
    if (u.state === "done" && affects(u.dir)) scheduleList();
  }

  let es = null;
  function setLive(on) {
    const el = $("live");
    el.classList.toggle("on", on);
    const label = on ? "Live updates on" : "Live updates reconnecting";
    el.title = label;
    el.setAttribute("aria-label", label);
  }

  function connectLive() {
    if (es) es.close();
    es = new EventSource("/api/events");
    let opened = false;
    es.addEventListener("open", () => {
      setLive(true);
      if (opened) scheduleList(0); // reconnected: catch up on anything missed
      opened = true;
    });
    es.addEventListener("error", () => setLive(false));
    es.addEventListener("change", (ev) => {
      const d = parse(ev.data);
      if (d && affects(d.dir || "")) scheduleList();
    });
    es.addEventListener("upload", (ev) => onUploadEvent(parse(ev.data)));
  }

  // ---------- wiring

  function wire() {
    if (!("webkitdirectory" in document.createElement("input"))) $("pick-folder-label").hidden = true;

    $("pick-files").addEventListener("change", (e) => {
      enqueue(fromFileList(e.target.files, false));
      e.target.value = "";
    });
    $("pick-folder").addEventListener("change", (e) => {
      enqueue(fromFileList(e.target.files, true));
      e.target.value = "";
    });

    $("text-toggle").addEventListener("click", () => toggleComposer($("composer").hidden));
    $("text-cancel").addEventListener("click", () => toggleComposer(false));
    $("composer").addEventListener("submit", (e) => {
      e.preventDefault();
      saveComposer();
    });
    $("text-input").addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        saveComposer();
      } else if (e.key === "Escape") {
        toggleComposer(false);
      }
    });

    $("home-link").addEventListener("click", (e) => {
      e.preventDefault();
      navigate([]);
    });
    $("queue-clear").addEventListener("click", () => {
      state.queue = state.queue.filter((i) => !FINISHED.has(i.state));
      renderQueue();
    });

    document.addEventListener("dragenter", (e) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      dragDepth++;
      showDrop(true);
    });
    document.addEventListener("dragover", (e) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
    });
    document.addEventListener("dragleave", (e) => {
      if (!hasFiles(e)) return;
      dragDepth = Math.max(0, dragDepth - 1);
      if (!dragDepth) showDrop(false);
    });
    document.addEventListener("drop", (e) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      dragDepth = 0;
      showDrop(false);
      collectDrop(e.dataTransfer).then(enqueue);
    });
    document.addEventListener("paste", onPaste);

    window.addEventListener("popstate", () => navigate(currentFromURL(), "none"));
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") {
        resumePaused();
        scheduleList(0);
      }
    });
    window.addEventListener("online", resumePaused);
    window.addEventListener("pagehide", () => {
      if (es) es.close();
      es = null;
      setLive(false);
    });
    window.addEventListener("pageshow", (e) => {
      if (e.persisted || !es) connectLive();
    });
    window.addEventListener("beforeunload", (e) => {
      if (state.queue.some((i) => i.state === "uploading" || i.state === "queued")) {
        e.preventDefault();
        e.returnValue = "";
      }
    });
    setInterval(renderListSoon, 30000); // keep "5 min ago" fresh
  }

  async function boot() {
    $("home-link").querySelector(".logo").append(icon("upload"));
    document.querySelector(".drop-icon").append(icon("upload"));
    wire();
    pruneSaved();
    state.dir = currentFromURL();
    render();
    loadList();
    try {
      Object.assign(state.config, await api("GET", "/api/config"));
    } catch {
      /* keep defaults */
    }
    $("version").textContent = "uped " + (state.config.version || "");
    const ttl = ttlText(state.config.ttlSeconds);
    $("empty-text").textContent = "Drop files here or tap Add files." + (ttl ? ` Everything disappears after ${ttl}.` : "");
    if (!es) connectLive();
  }

  boot();
})();
