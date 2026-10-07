import { test as base, expect, type APIRequestContext, type Locator, type Page, type TestInfo } from "@playwright/test";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { once } from "node:events";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

export { expect };

export const REPO_ROOT = path.join(__dirname, "..", "..");
/** The data directory of the server started by playwright.config.ts. */
export const DATA_DIR = path.join(__dirname, "..", ".e2e-data");
/** Chunk size the config starts the server with (--chunk-size 4M). */
export const CHUNK = 4 << 20;
export const NBSP = " ";

// ---------- fixture files

/** Deterministic pseudo-random bytes (xorshift32), different for each seed. */
export function bytes(size: number, seed = 1): Buffer {
  const buf = Buffer.alloc(size);
  let x = (Math.imul(seed, 2654435761) >>> 0) || 1;
  const next = () => {
    x ^= x << 13;
    x ^= x >>> 17;
    x ^= x << 5;
    return (x >>>= 0);
  };
  let i = 0;
  for (; i + 4 <= size; i += 4) buf.writeUInt32LE(next(), i);
  for (; i < size; i++) buf[i] = next() & 0xff;
  return buf;
}

export const sha256 = (data: Buffer | string) => createHash("sha256").update(data).digest("hex");

/** Writes a file under the test's output folder and returns its path. */
export function fixture(testInfo: TestInfo, rel: string, content: string | Buffer): string {
  const p = testInfo.outputPath("fixtures", rel);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, content);
  return p;
}

/** A folder tree: strings are file contents, objects are subfolders. */
export type Tree = { [name: string]: string | Tree };

/** Writes a tree under the test's output folder and returns its root path. */
export function fixtureTree(testInfo: TestInfo, name: string, tree: Tree): string {
  const root = testInfo.outputPath("fixtures", name);
  const write = (dir: string, t: Tree) => {
    fs.mkdirSync(dir, { recursive: true });
    for (const [k, v] of Object.entries(t)) {
      if (typeof v === "string") fs.writeFileSync(path.join(dir, k), v);
      else write(path.join(dir, k), v);
    }
  };
  write(root, tree);
  return root;
}

// ---------- server API

export type Entry = {
  name: string;
  type: "file" | "dir";
  size: number;
  items: number;
  modified: string;
  expiresAt?: string;
  device?: string;
  preview?: string;
};
export type UploadInfo = { id: string; name: string; dir: string; size: number; offset: number; device?: string; state: string };
export type Listing = { path: string; entries: Entry[]; uploads: UploadInfo[]; free: number };

const encodeRel = (rel: string) => rel.split("/").filter(Boolean).map(encodeURIComponent).join("/");

export async function apiList(request: APIRequestContext, dir = ""): Promise<Listing> {
  const res = await request.get("/api/list?path=" + encodeURIComponent(dir));
  expect(res.status(), `GET /api/list?path=${dir}`).toBe(200);
  return res.json();
}

/** Names in a folder, sorted, or null when the folder does not exist. */
export async function namesIn(request: APIRequestContext, dir = ""): Promise<string[] | null> {
  const res = await request.get("/api/list?path=" + encodeURIComponent(dir));
  if (res.status() === 404) return null;
  expect(res.status(), `GET /api/list?path=${dir}`).toBe(200);
  return ((await res.json()) as Listing).entries.map((e) => e.name).sort();
}

/** Uploads one file through the chunk protocol and returns its final path. */
export async function apiUpload(request: APIRequestContext, dir: string, name: string, content: string | Buffer): Promise<string> {
  const body = Buffer.from(content);
  const created = await request.post("/api/uploads", { data: { name, dir, size: body.length } });
  expect(created.status(), "create upload").toBe(201);
  const { id } = await created.json();
  for (let off = 0; off < body.length; off += CHUNK) {
    const put = await request.put(`/api/uploads/${id}?offset=${off}`, {
      data: body.subarray(off, off + CHUNK),
      headers: { "Content-Type": "application/offset+octet-stream" },
    });
    expect(put.status(), "upload chunk").toBe(204);
  }
  const done = await request.post(`/api/uploads/${id}/finish`);
  expect(done.status(), "finish upload").toBe(200);
  return (await done.json()).path;
}

export async function download(request: APIRequestContext, rel: string): Promise<Buffer> {
  const res = await request.get("/d/" + encodeRel(rel));
  expect(res.status(), `GET /d/${rel}`).toBe(200);
  return res.body();
}

/** Cancels every upload and deletes every item, leaving an empty drop. */
export async function resetServer(request: APIRequestContext): Promise<void> {
  const root = await apiList(request, "");
  for (const u of root.uploads) await request.delete("/api/uploads/" + encodeURIComponent(u.id));
  for (const e of root.entries) {
    const res = await request.delete("/api/items?path=" + encodeURIComponent(e.name));
    expect([204, 404], `delete ${e.name}`).toContain(res.status());
  }
}

/** Matches the chunk PUTs (/api/uploads/<id>?offset=N), for page.route. */
export const isChunk = (url: URL) => /^\/api\/uploads\/[^/]+$/.test(url.pathname) && url.searchParams.has("offset");

// ---------- page helpers

export const row = (page: Page, name: string) => page.locator(`#list li[data-name=${JSON.stringify(name)}]`);
export const fileRow = (page: Page, name: string) => page.locator(`#list li.item.file[data-name=${JSON.stringify(name)}]`);
export const dirRow = (page: Page, name: string) => page.locator(`#list li.item.dir[data-name=${JSON.stringify(name)}]`);

/** Text of an element with the non-breaking spaces of meta lines made plain. */
export async function plainText(loc: Locator): Promise<string> {
  return ((await loc.textContent()) || "").replaceAll(NBSP, " ");
}

/** Waits until this tab's upload queue has finished n items. */
export async function waitUploaded(page: Page, n: number) {
  await expect(page.locator('#queue .qitem[data-state="done"]')).toHaveCount(n, { timeout: 15_000 });
}

/** Starts a drag over the page without dropping, which shows the overlay. */
export async function dragEnter(page: Page) {
  await page.evaluate(() => {
    const dt = new DataTransfer();
    dt.items.add(new File(["x"], "x.txt"));
    document.body.dispatchEvent(new DragEvent("dragenter", { bubbles: true, cancelable: true, dataTransfer: dt }));
  });
}

/** Drops files onto the page with a real DataTransfer, as a desktop browser does. */
export async function dropFiles(page: Page, files: { name: string; content: string; type?: string }[]) {
  await page.evaluate((files) => {
    const dt = new DataTransfer();
    for (const f of files) dt.items.add(new File([f.content], f.name, { type: f.type || "" }));
    for (const type of ["dragenter", "dragover", "drop"]) {
      document.body.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }));
    }
  }, files);
}

/**
 * Drops a folder onto the page. A real DataTransfer cannot carry directory
 * entries, so this dispatches a plain drop event whose dataTransfer holds
 * stand-in FileSystemEntry objects. Each folder hands out its children in
 * batches of batchSize per readEntries call, as Chrome does with 100.
 */
export async function dropFolder(page: Page, name: string, tree: Tree, batchSize = 2) {
  await page.evaluate(({ name, tree, batchSize }) => {
    type T = { [k: string]: string | T };
    const fileEntry = (n: string, text: string) => ({
      isFile: true,
      isDirectory: false,
      name: n,
      file: (ok: (f: File) => void) => ok(new File([text], n)),
    });
    const dirEntry = (n: string, t: T): object => {
      const kids = Object.entries(t).map(([k, v]) => (typeof v === "string" ? fileEntry(k, v) : dirEntry(k, v)));
      const batches: object[][] = [];
      for (let i = 0; i < kids.length; i += batchSize) batches.push(kids.slice(i, i + batchSize));
      return {
        isFile: false,
        isDirectory: true,
        name: n,
        createReader() {
          let i = 0;
          return { readEntries: (ok: (b: object[]) => void) => setTimeout(() => ok(batches[i++] || []), 5) };
        },
      };
    };
    const dt = { types: ["Files"], dropEffect: "none", files: [], items: [{ kind: "file", webkitGetAsEntry: () => dirEntry(name, tree) }] };
    const ev = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "dataTransfer", { value: dt });
    document.body.dispatchEvent(ev);
  }, { name, tree, batchSize });
}

/** Dispatches a paste event outside any input, carrying text or files. */
export async function paste(page: Page, data: { text?: string; files?: { name: string; type: string; base64: string }[] }, target = "body") {
  await page.evaluate(({ data, target }) => {
    const dt = new DataTransfer();
    if (data.text != null) dt.setData("text/plain", data.text);
    for (const f of data.files || []) {
      const bin = Uint8Array.from(atob(f.base64), (c) => c.charCodeAt(0));
      dt.items.add(new File([bin], f.name, { type: f.type }));
    }
    document.querySelector(target)!.dispatchEvent(new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData: dt }));
  }, { data, target });
}

/**
 * The device label the server derives from this project's user agent.
 * Playwright's "Desktop Chrome" descriptor sends a Windows user agent.
 */
export const deviceLabel = (testInfo: TestInfo) => (testInfo.project.name === "chromium-mobile" ? "iPhone Safari" : "Windows Chrome");

/**
 * A regex for a meta line, written with plain spaces. Meta lines join words
 * with non-breaking spaces, so every space here matches any whitespace.
 */
export const metaRe = (source: string) => new RegExp(source.replaceAll(" ", "\\s"));

// ---------- extra servers (limits.spec.ts)

let binary: Promise<string> | undefined;

/** Builds uped once per worker (UPED_BIN skips the build). */
function uped(): Promise<string> {
  if (process.env.UPED_BIN) return Promise.resolve(path.resolve(process.env.UPED_BIN));
  binary ??= (async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "uped-e2e-bin-"));
    process.on("exit", () => fs.rmSync(dir, { recursive: true, force: true }));
    const out = path.join(dir, "uped");
    const go = spawn("go", ["build", "-o", out, "./cmd/uped"], { cwd: REPO_ROOT, stdio: ["ignore", "inherit", "inherit"] });
    const [code] = await once(go, "exit");
    if (code !== 0) throw new Error(`go build exited with ${code}`);
    return out;
  })();
  return binary;
}

export type ExtraServer = { url: string; dataDir: string; stop: () => Promise<void> };

/** Starts another uped on a free port with its own data folder. */
export async function startServer(args: string[]): Promise<ExtraServer> {
  const bin = await uped();
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "uped-e2e-data-"));
  const proc = spawn(bin, ["--listen", "127.0.0.1:0", "--data", dataDir, ...args], { stdio: ["ignore", "ignore", "pipe"] });
  let log = "";
  const url = await new Promise<string>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("uped did not start:\n" + log)), 15_000);
    proc.stderr!.on("data", (d) => {
      log += d;
      const m = /listen=127\.0\.0\.1:(\d+)/.exec(log);
      if (m) {
        clearTimeout(timer);
        resolve(`http://127.0.0.1:${m[1]}`);
      }
    });
    proc.on("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`uped exited with ${code}:\n${log}`));
    });
  });
  return {
    url,
    dataDir,
    stop: async () => {
      if (proc.exitCode === null && proc.signalCode === null) {
        proc.kill("SIGTERM");
        await once(proc, "exit");
      }
      fs.rmSync(dataDir, { recursive: true, force: true });
    },
  };
}

// ---------- fixtures

export type Guard = {
  /** Lets console errors matching re pass, e.g. an expected 404. */
  allow: (re: RegExp) => void;
  /** Watches another page (from another context) for errors too. */
  watch: (page: Page) => void;
};

export const test = base.extend<{ cleanServer: void; guard: Guard }>({
  // Every test starts with an empty drop on the shared server.
  cleanServer: [
    async ({ request }, use) => {
      await resetServer(request);
      await use();
    },
    { auto: true },
  ],
  // Fails a test that logs an unexpected console error or throws in the
  // page, and accepts confirm() prompts (delete asks first).
  guard: [
    async ({ page }, use) => {
      const errors: string[] = [];
      const allowed: RegExp[] = [];
      const watch = (p: Page) => {
        p.on("console", (m) => {
          if (m.type() === "error") errors.push(`${m.text()} @ ${m.location().url}`);
        });
        p.on("pageerror", (e) => errors.push("page error: " + String(e)));
        p.on("dialog", (d) => d.accept());
      };
      watch(page);
      await use({ allow: (re) => allowed.push(re), watch });
      expect(errors.filter((e) => !allowed.some((re) => re.test(e))), "console errors").toEqual([]);
    },
    { auto: true },
  ],
});
