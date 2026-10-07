import fs from "node:fs";
import path from "node:path";
import type { Page, Response } from "@playwright/test";
import { CHUNK, DATA_DIR, bytes, download, expect, fixture, isChunk, namesIn, sha256, test, waitUploaded } from "./helpers";

// The server runs with 4 MiB chunks, so a 20 MiB file takes five PUTs.
const SIZE = 20 << 20;

const isCreate = (r: Response) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/uploads";

/** Resolves to the id of the next upload this page creates. */
async function nextUploadId(page: Page): Promise<string> {
  return (await (await page.waitForResponse(isCreate)).json()).id;
}

test("a chunk lost once is retried and the file arrives intact", async ({ page, request, guard }, testInfo) => {
  guard.allow(/net::ERR_/);
  const data = bytes(SIZE, 3);
  const file = fixture(testInfo, "big.bin", data);
  let puts = 0;
  await page.route(isChunk, (route) => (++puts === 2 ? route.abort("connectionreset") : route.continue()));

  await page.goto("/");
  await page.setInputFiles("#pick-files", file);
  await waitUploaded(page, 1);
  expect(puts).toBe(6); // five chunks plus the retried one
  expect(sha256(await download(request, "big.bin"))).toBe(sha256(data));
});

test("after a reload, picking the same file resumes from the server's offset", async ({ page, request, guard }, testInfo) => {
  guard.allow(/net::ERR_/);
  const data = bytes(SIZE, 4);
  const file = fixture(testInfo, "resume.bin", data);
  // The first chunk gets through; every later one fails.
  let puts = 0;
  await page.route(isChunk, (route) => (++puts === 1 ? route.continue() : route.abort("connectionreset")));

  await page.goto("/");
  const created = nextUploadId(page);
  await page.setInputFiles("#pick-files", file);
  const id = await created;

  // The app retries after 0, 1, 3, 5 and 10 s, then pauses. An "online"
  // event wakes a waiting retry at once, as when a phone reconnects, so
  // send one whenever the upload is not paused yet.
  await page.waitForFunction(
    () => {
      const item = document.querySelector<HTMLElement>("#queue .qitem");
      if (item?.dataset.state === "paused") return true;
      window.dispatchEvent(new Event("online"));
      return false;
    },
    null,
    { polling: 50, timeout: 30_000 },
  );
  await expect(page.locator("#queue .qitem .qstatus")).toContainText("Tap Retry to continue.");
  await expect(page.locator('#queue button[title="Retry"]')).toBeVisible();
  expect(puts).toBe(7); // one good chunk, then six failures: the first try and five retries

  // The server kept the first chunk.
  const head = await request.head(`/api/uploads/${id}`);
  expect(head.status()).toBe(200);
  expect(head.headers()["upload-offset"]).toBe(String(CHUNK));
  expect(head.headers()["upload-length"]).toBe(String(SIZE));

  await page.unroute(isChunk);
  await page.reload();
  const calls: string[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/uploads")) calls.push(`${r.method()} ${u.pathname}${u.search}`);
  });
  await page.setInputFiles("#pick-files", file);
  await waitUploaded(page, 1);

  expect(calls).toEqual([
    `HEAD /api/uploads/${id}`,
    `PUT /api/uploads/${id}?offset=${CHUNK}`,
    `PUT /api/uploads/${id}?offset=${2 * CHUNK}`,
    `PUT /api/uploads/${id}?offset=${3 * CHUNK}`,
    `PUT /api/uploads/${id}?offset=${4 * CHUNK}`,
    `POST /api/uploads/${id}/finish`,
  ]);
  expect(sha256(await download(request, "resume.bin"))).toBe(sha256(data));
  // A finished upload leaves no resume note behind.
  expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("uped.up.")))).toEqual([]);
});

test("Cancel deletes the partial upload on the server", async ({ page, request }, testInfo) => {
  const file = fixture(testInfo, "cancel.bin", bytes(3 * CHUNK, 5));
  // Hold the second chunk so the upload is mid-way when Cancel is clicked.
  let release = () => {};
  const gate = new Promise<void>((r) => (release = r));
  let puts = 0;
  await page.route(isChunk, async (route) => {
    if (++puts >= 2) await gate;
    await route.continue().catch(() => {}); // the page may have aborted it meanwhile
  });

  await page.goto("/");
  const created = nextUploadId(page);
  await page.setInputFiles("#pick-files", file);
  const id = await created;
  const part = path.join(DATA_DIR, ".parts", id + ".part");
  await expect.poll(() => fs.existsSync(part) && fs.statSync(part).size).toBe(CHUNK);
  await expect(page.locator('#queue .qitem[data-state="uploading"]')).toHaveCount(1);
  await expect(page).toHaveTitle(/^\d+% · uped$/);

  const deleted = page.waitForResponse((r) => r.request().method() === "DELETE");
  await page.locator('#queue button[title="Cancel"]').click();
  expect((await deleted).status()).toBe(204);
  release();

  await expect(page.locator('#queue .qitem[data-state="cancelled"] .qstatus')).toHaveText("Cancelled");
  await expect(page).toHaveTitle("uped");
  expect(fs.existsSync(part)).toBe(false);
  expect(fs.existsSync(path.join(DATA_DIR, ".parts", id + ".json"))).toBe(false);
  expect((await request.head(`/api/uploads/${id}`)).status()).toBe(404);
  expect(await namesIn(request, "")).toEqual([]);
});

test("picking a file the server already has skips it", async ({ page, request }, testInfo) => {
  const file = fixture(testInfo, "same.bin", bytes(100_000, 6));
  await page.goto("/");
  await page.setInputFiles("#pick-files", file);
  await waitUploaded(page, 1);

  const puts: string[] = [];
  page.on("request", (r) => r.method() === "PUT" && puts.push(r.url()));
  await page.setInputFiles("#pick-files", file);
  await expect(page.locator('#queue .qitem[data-state="skipped"] .qstatus')).toHaveText("Already on the server");
  expect(puts).toEqual([]);
  expect(await namesIn(request, "")).toEqual(["same.bin"]);
});

test("a lost answer to finish does not duplicate the file", async ({ page, request, guard }, testInfo) => {
  guard.allow(/net::ERR_|status of 404/);
  const data = bytes(CHUNK + 1000, 7);
  const file = fixture(testInfo, "lost.bin", data);
  // The server finishes the upload, but the browser never hears back.
  await page.route(
    (url) => url.pathname.endsWith("/finish"),
    async (route) => {
      await route.fetch();
      await route.abort("connectionreset");
    },
    { times: 1 },
  );

  await page.goto("/");
  await page.setInputFiles("#pick-files", file);
  // The retry gets 404 (the upload is finished), so the app asks to create
  // the file again; the server recognises it and answers "done".
  await expect(page.locator('#queue .qitem[data-state="done"] .qstatus')).toHaveText(/^Uploaded · /, { timeout: 15_000 });
  expect(await namesIn(request, "")).toEqual(["lost.bin"]);
  expect(sha256(await download(request, "lost.bin"))).toBe(sha256(data));
});
