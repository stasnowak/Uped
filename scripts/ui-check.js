// Browser check of the uped web UI with headless Chromium (Playwright).
// Interim until the Playwright suite in e2e/ (Phase 4 of the plan) exists;
// port these checks there, then delete this file and ui-check.sh.
//
// Usage: node scripts/ui-check.js BASE_URL [SCREENSHOT_DIR]
// Start the server with --chunk-size 1M so the incoming-upload check sees
// several chunks. scripts/ui-check.sh does all of that for you.
const { chromium, devices } = require("playwright");
const fs = require("fs");
const os = require("os");
const path = require("path");

const base = process.argv[2];
if (!base) {
  console.error("usage: node scripts/ui-check.js BASE_URL [SCREENSHOT_DIR]");
  process.exit(2);
}
const shots = process.argv[3] || fs.mkdtempSync(path.join(os.tmpdir(), "uped-shots-"));
const fx = fs.mkdtempSync(path.join(os.tmpdir(), "uped-fixtures-"));
fs.mkdirSync(path.join(fx, "trip", "day1"), { recursive: true });
fs.writeFileSync(path.join(fx, "a.txt"), "alpha\n");
fs.writeFileSync(path.join(fx, "photo.jpg"), require("crypto").randomBytes(300000));
fs.writeFileSync(path.join(fx, "trip", "top.jpg"), "one");
fs.writeFileSync(path.join(fx, "trip", "day1", "b.jpg"), "two");
const results = [];
const ok = (name, cond, extra = "") => { results.push([cond ? "ok  " : "FAIL", name, extra]); if (!cond) process.exitCode = 1; };

(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1100, height: 800 } });
  const page = await ctx.newPage();
  const errors = [];
  const expected404 = [];
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    const url = (m.location() && m.location().url) || "";
    // The deliberate visit to a missing folder gets a 404 for its listing.
    if (url.includes("/api/list?path=nope")) { expected404.push(url); return; }
    errors.push(m.text() + " @ " + url);
  });
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("dialog", (d) => d.accept());

  await page.goto(base + "/");
  ok("title is uped", (await page.title()) === "uped", await page.title());
  await page.waitForSelector("#empty:not([hidden])", { timeout: 5000 });
  ok("empty state mentions 7 days", (await page.textContent("#empty-text")).includes("7 days"), await page.textContent("#empty-text"));
  await page.screenshot({ path: path.join(shots, "1-empty-desktop.png") });

  // Clicking the visible buttons opens real file choosers.
  for (const [sel, label] of [["label:has(#pick-files)", "Add files"], ["#pick-folder-label", "Add folder"]]) {
    const [chooser] = await Promise.all([page.waitForEvent("filechooser", { timeout: 3000 }), page.click(sel)]);
    ok(label + " opens a multi-select file chooser", chooser.isMultiple());
    if (label === "Add files") await chooser.setFiles([]); // a folder chooser cannot be answered empty; leave it
  }

  // Verification: two files via the picker -> two rows within 5 s.
  await page.setInputFiles("#pick-files", [path.join(fx, "a.txt"), path.join(fx, "photo.jpg")]);
  await page.waitForFunction(() => document.querySelectorAll("#list li.item.file").length === 2, null, { timeout: 5000 });
  ok("two picked files appear as rows", true);
  await page.waitForSelector('#queue .qitem[data-state="done"]', { timeout: 5000 });
  ok("queue shows done items", (await page.$$('#queue .qitem[data-state="done"]')).length === 2);
  ok("text preview rendered", (await page.textContent('li[data-name="a.txt"] .preview')).trim() === "alpha");

  // Folder picker keeps structure.
  await page.setInputFiles("#pick-folder", path.join(fx, "trip"));
  await page.waitForSelector('li.item.dir[data-name="trip"]', { timeout: 5000 });
  const tripMeta = (await page.textContent('li.item.dir[data-name="trip"] .meta')).replace(/\u00a0/g, " ");
  ok("folder row with 2 files", tripMeta.startsWith("2 files"), tripMeta);

  // Text composer.
  await page.click("#text-toggle");
  await page.fill("#text-input", "https://www.example.com/some/page");
  await page.click("#text-save");
  await page.waitForSelector('li[data-name="link-example.com.txt"]', { timeout: 5000 });
  ok("text saved as link-example.com.txt", true);
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(shots, "2-list-desktop.png"), fullPage: true });

  // Second tab sees live updates.
  const other = await ctx.newPage();
  other.on("pageerror", (e) => errors.push("other: " + String(e)));
  await other.goto(base + "/");
  await other.waitForSelector('li[data-name="trip"]');
  await page.setInputFiles("#pick-files", { name: "live.bin", mimeType: "application/octet-stream", buffer: Buffer.alloc(5000, 7) });
  await other.waitForSelector('li[data-name="live.bin"]', { timeout: 5000 });
  ok("other tab sees new file without reload", true);

  // Navigate into folder, breadcrumbs, back button.
  await page.click('li.item.dir[data-name="trip"] a.name');
  await page.waitForSelector('li.item.dir[data-name="day1"]', { timeout: 5000 });
  ok("URL has ?path=trip", page.url().endsWith("/?path=trip"), page.url());
  ok("breadcrumb shows trip", (await page.textContent("#crumbs [aria-current]")) === "trip");
  await page.goBack();
  await page.waitForSelector('li[data-name="a.txt"]', { timeout: 5000 });
  ok("back returns to root", page.url().endsWith("/"), page.url());
  await page.goto(base + "/?path=trip/day1");
  await page.waitForSelector('li[data-name="b.jpg"]', { timeout: 5000 });
  ok("deep link to trip/day1 works", true);
  await page.goto(base + "/?path=nope");
  await page.waitForFunction(() => location.search === "", null, { timeout: 5000 });
  ok("missing folder falls back to Home", true);

  // Copy button (insecure-context fallback path).
  await page.waitForSelector('li[data-name="a.txt"] button[title="Copy text"]');
  await page.click('li[data-name="a.txt"] button[title="Copy text"]');
  await page.waitForFunction(() => document.getElementById("toast").textContent === "Copied" || document.getElementById("copy-dialog").open, null, { timeout: 3000 }).catch(() => {});
  const toastText = await page.textContent("#toast");
  const dialogOpen = await page.evaluate(() => document.getElementById("copy-dialog").open);
  ok("copy gives a toast or the manual-copy dialog", toastText === "Copied" || dialogOpen, `toast=${toastText} dialog=${dialogOpen}`);
  if (dialogOpen) await page.keyboard.press("Escape");

  // Delete with confirm.
  await page.click('li[data-name="photo.jpg"] button[title="Delete"]');
  await page.waitForFunction(() => !document.querySelector('li[data-name="photo.jpg"]'), null, { timeout: 5000 });
  ok("delete removes the row", true);
  await other.waitForFunction(() => !document.querySelector('li[data-name="photo.jpg"]'), null, { timeout: 5000 });
  ok("delete disappears in the other tab", true);

  // Download link points at /d/ with a download attribute.
  const href = await page.getAttribute('li[data-name="a.txt"] a.name', "href");
  ok("file link is /d/a.txt", href === "/d/a.txt", href);
  const resp = await page.request.get(base + href);
  ok("download returns the content", (await resp.text()) === "alpha\n");

  // Synthetic drop of two files onto the page.
  await page.evaluate(() => {
    const dt = new DataTransfer();
    dt.items.add(new File(["dropped one"], "drop1.txt", { type: "text/plain" }));
    dt.items.add(new File(["dropped two"], "drop2.txt", { type: "text/plain" }));
    for (const type of ["dragenter", "dragover", "drop"]) {
      document.body.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }));
    }
  });
  await page.waitForSelector('li[data-name="drop2.txt"]', { timeout: 5000 });
  ok("dropped files uploaded", true);
  ok("drop overlay hidden after drop", await page.$eval("#drop", (el) => el.hidden));

  // Folder drop: a stand-in DataTransfer whose folder entry returns its
  // children over several readEntries calls, like Chrome's 100-per-batch.
  await page.evaluate(() => {
    const fileEntry = (name, text) => ({ isFile: true, isDirectory: false, name, file: (ok) => ok(new File([text], name)) });
    const dirEntry = (name, batches) => ({
      isFile: false, isDirectory: true, name,
      createReader() { let i = 0; return { readEntries: (ok) => setTimeout(() => ok(batches[i++] || []), 5) }; },
    });
    const album = dirEntry("album", [
      [fileEntry("p1.jpg", "1"), fileEntry("p2.jpg", "2")],
      [fileEntry("p3.jpg", "3"), dirEntry("raw", [[fileEntry("p1.dng", "raw1")]])],
    ]);
    const dt = { types: ["Files"], dropEffect: "none", files: [], items: [{ kind: "file", webkitGetAsEntry: () => album }] };
    const ev = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "dataTransfer", { value: dt });
    document.body.dispatchEvent(ev);
  });
  await page.waitForSelector('li.item.dir[data-name="album"]', { timeout: 5000 });
  await page.waitForFunction(() => (document.querySelector('li.item.dir[data-name="album"] .meta') || {}).textContent?.startsWith("4" + String.fromCharCode(160) + "files"), null, { timeout: 5000 });
  ok("dropped folder kept structure (4 files across batches and a subfolder)", true);
  const rawList = await (await page.request.get(base + "/api/list?path=album/raw")).json();
  ok("nested folder album/raw holds p1.dng", rawList.entries.length === 1 && rawList.entries[0].name === "p1.dng", JSON.stringify(rawList.entries.map((e) => e.name)));

  // Incoming row in the other tab while this tab's upload is slowed down.
  let delayed = 0;
  await page.route("**/api/uploads/*?offset=*", async (route) => {
    if (delayed++ === 1) await new Promise((r) => setTimeout(r, 2500));
    await route.continue();
  });
  await page.setInputFiles("#pick-files", { name: "slow.bin", mimeType: "application/octet-stream", buffer: Buffer.alloc(3 * 1024 * 1024, 9) });
  await other.waitForSelector('li.item.incoming[data-name="slow.bin"]', { timeout: 5000 });
  await other.waitForFunction(() => {
    const m = document.querySelector('li.item.incoming[data-name="slow.bin"] .meta');
    return m && /[1-9]\d*%$/.test(m.textContent);
  }, null, { timeout: 5000 });
  const incomingMeta = (await other.textContent('li.item.incoming[data-name="slow.bin"] .meta')).replace(/\u00a0/g, " ");
  ok("other tab shows the incoming upload with rising progress", /^Incoming from .* [1-9]\d*%$/.test(incomingMeta), incomingMeta);
  await other.screenshot({ path: path.join(shots, "7-incoming.png") });
  await other.waitForSelector('li.item.file[data-name="slow.bin"]', { timeout: 10000 });
  ok("incoming row turns into a file row when done", !(await other.$('li.item.incoming[data-name="slow.bin"]')));
  await page.unroute("**/api/uploads/*?offset=*");

  // Paste text outside inputs.
  await page.evaluate(() => {
    const dt = new DataTransfer();
    dt.setData("text/plain", "Pasted note\nsecond line");
    document.body.dispatchEvent(new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData: dt }));
  });
  await page.waitForSelector('li[data-name="pasted-note.txt"]', { timeout: 5000 });
  ok("pasted text saved", true);

  ok("title back to uped when idle", (await page.title()) === "uped", await page.title());
  ok("no console errors (besides the deliberate missing-folder 404)", errors.length === 0, errors.join(" | ") + (expected404.length ? " expected: " + expected404.join(",") : ""));

  // Phone viewport, light and dark.
  for (const scheme of ["light", "dark"]) {
    const m = await browser.newContext({ ...devices["iPhone 13"], colorScheme: scheme });
    const p = await m.newPage();
    const errs = [];
    p.on("pageerror", (e) => errs.push(String(e)));
    await p.goto(base + "/");
    await p.waitForSelector('li[data-name="trip"]');
    const overflow = await p.evaluate(() => document.scrollingElement.scrollWidth - window.innerWidth);
    ok(`no horizontal scroll on phone (${scheme})`, overflow <= 0, "overflow " + overflow);
    const small = await p.$$eval(".btn, .icon-btn", (els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 44).map((e) => e.textContent || e.getAttribute("aria-label")));
    ok(`tap targets at least 44px (${scheme})`, small.length === 0, small.join(", "));
    await p.screenshot({ path: path.join(shots, `3-phone-${scheme}.png`), fullPage: true });
    ok(`no page errors on phone (${scheme})`, errs.length === 0, errs.join(" | "));
    await m.close();
  }
  // 360 px wide, the plan's narrowest target.
  const narrow = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const np = await narrow.newPage();
  await np.goto(base + "/");
  await np.waitForSelector('li[data-name="trip"]');
  const over360 = await np.evaluate(() => document.scrollingElement.scrollWidth - window.innerWidth);
  ok("no horizontal scroll at 360px", over360 <= 0, "overflow " + over360);
  await np.screenshot({ path: path.join(shots, "4-360.png"), fullPage: true });

  // Drop overlay screenshot (dark desktop).
  const dark = await browser.newContext({ viewport: { width: 1100, height: 700 }, colorScheme: "dark" });
  const dp = await dark.newPage();
  await dp.goto(base + "/");
  await dp.waitForSelector('li[data-name="trip"]');
  await dp.screenshot({ path: path.join(shots, "5-desktop-dark.png") });
  await dp.evaluate(() => {
    const dt = new DataTransfer();
    dt.items.add(new File(["x"], "x.txt"));
    document.body.dispatchEvent(new DragEvent("dragenter", { bubbles: true, cancelable: true, dataTransfer: dt }));
  });
  await dp.screenshot({ path: path.join(shots, "6-drop-overlay-dark.png") });

  await browser.close();
  for (const [s, n, x] of results) console.log(s, n, x ? "· " + x : "");
  console.log(`${results.filter((r) => r[0] === "ok  ").length} of ${results.length} checks passed; screenshots in ${shots}`);
  fs.rmSync(fx, { recursive: true, force: true });
})().catch((e) => { console.error(e); process.exit(1); });
