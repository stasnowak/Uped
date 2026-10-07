import type { Browser, Page, TestInfo } from "@playwright/test";
import { CHUNK, apiUpload, bytes, expect, fileRow, fixture, isChunk, metaRe, row, test, waitUploaded, type Guard } from "./helpers";

/** Opens the drop in a second browser context: another device on the LAN. */
async function otherDevice(browser: Browser, testInfo: TestInfo, guard: Guard, path = "/"): Promise<Page> {
  const ctx = await browser.newContext({ baseURL: testInfo.project.use.baseURL, viewport: { width: 1000, height: 700 } });
  const page = await ctx.newPage();
  guard.watch(page);
  await page.goto(path);
  await expect(page.locator("#live.on")).toHaveCount(1); // the event stream is up
  return page;
}

test("a file uploaded on one device appears on another without a reload", async ({ page, browser, guard }, testInfo) => {
  const other = await otherDevice(browser, testInfo, guard);
  await expect(other.locator("#empty")).toBeVisible();

  await page.goto("/");
  await page.setInputFiles("#pick-files", fixture(testInfo, "live.bin", bytes(5000, 1)));
  await expect(fileRow(other, "live.bin")).toBeVisible({ timeout: 5_000 });
  await expect(other.locator("#empty")).toBeHidden();
  await other.context().close();
});

test("another device sees an incoming upload with rising progress", async ({ page, browser, guard }, testInfo) => {
  const other = await otherDevice(browser, testInfo, guard);
  // Progress events go out at most once a second per upload, so space the
  // chunks out to give the other device several to see.
  await page.route(isChunk, async (route) => {
    if (Number(new URL(route.request().url()).searchParams.get("offset")) > 0) await new Promise((r) => setTimeout(r, 1200));
    await route.continue();
  });
  await page.goto("/");
  await page.setInputFiles("#pick-files", fixture(testInfo, "slow.bin", bytes(4 * CHUNK, 2)));

  const incoming = other.locator('#list li.item.incoming[data-name="slow.bin"]');
  await expect(incoming).toBeVisible({ timeout: 5_000 });
  await expect(incoming.locator(".meta")).toHaveText(metaRe("^Incoming from \\S.* · 16\\.0 MB · [1-9]\\d*%$"));
  // Record each percentage shown until two different non-zero ones appeared.
  const seen: number[] = [];
  await expect
    .poll(
      async () => {
        const meta = await other.evaluate(() => document.querySelector('#list li.item.incoming[data-name="slow.bin"] .meta')?.textContent ?? "");
        const pct = Number(/(\d+)%$/.exec(meta)?.[1] ?? 0);
        if (pct > 0 && seen.at(-1) !== pct) seen.push(pct);
        return seen.length;
      },
      { intervals: [100] },
    )
    .toBeGreaterThanOrEqual(2);
  expect(seen).toEqual([...seen].sort((a, b) => a - b));
  // The uploading tab does not list its own upload as incoming.
  await expect(page.locator("#list li.item.incoming")).toHaveCount(0);

  // When it finishes, the incoming row turns into a normal file row.
  await expect(fileRow(other, "slow.bin")).toBeVisible({ timeout: 15_000 });
  await expect(incoming).toHaveCount(0);
  await waitUploaded(page, 1);
  await other.context().close();
});

test("an upload into a subfolder shows as incoming in the parent folder", async ({ page, browser, guard, request }, testInfo) => {
  await apiUpload(request, "photos", "old.jpg", "o");
  const other = await otherDevice(browser, testInfo, guard);
  await page.route(isChunk, async (route) => {
    await new Promise((r) => setTimeout(r, 1500));
    await route.continue();
  });
  await page.goto("/?path=photos");
  await expect(row(page, "old.jpg")).toBeVisible();
  await page.setInputFiles("#pick-files", fixture(testInfo, "new.jpg", bytes(1000, 3)));
  // At the root, the incoming row names the subfolder.
  await expect(other.locator('#list li.item.incoming[data-name="photos/new.jpg"]')).toBeVisible({ timeout: 5_000 });
  await expect(other.locator('#list li.item.incoming[data-name="photos/new.jpg"] .name')).toHaveText("photos/new.jpg");
  await expect(other.locator('#list li.item.incoming[data-name="photos/new.jpg"]')).toHaveCount(0, { timeout: 10_000 });
  await expect(row(other, "photos").locator(".meta")).toHaveText(metaRe("^2 files · "));
  await other.context().close();
});

test("a delete on one device disappears on another", async ({ page, browser, guard, request }, testInfo) => {
  await apiUpload(request, "", "gone.txt", "bye");
  await apiUpload(request, "", "stays.txt", "hi");
  await apiUpload(request, "trip", "a.jpg", "a");
  const other = await otherDevice(browser, testInfo, guard);
  await expect(fileRow(other, "gone.txt")).toBeVisible();

  await page.goto("/");
  await fileRow(page, "gone.txt").locator('button[title="Delete"]').click();
  await expect(fileRow(other, "gone.txt")).toHaveCount(0, { timeout: 5_000 });
  await row(page, "trip").locator('button[title="Delete folder"]').click();
  await expect(row(other, "trip")).toHaveCount(0, { timeout: 5_000 });
  await expect(fileRow(other, "stays.txt")).toBeVisible();
  await other.context().close();
});

test("a device viewing a deleted folder is sent back home", async ({ page, browser, guard, request }, testInfo) => {
  await apiUpload(request, "trip", "a.jpg", "a");
  guard.allow(/\/api\/list\?path=trip/);
  const other = await otherDevice(browser, testInfo, guard, "/?path=trip");
  await expect(fileRow(other, "a.jpg")).toBeVisible();

  await page.goto("/");
  await row(page, "trip").locator('button[title="Delete folder"]').click();
  await expect(other.locator("#toast")).toContainText("That folder is gone", { timeout: 5_000 });
  await expect(other.locator("#crumbs [aria-current]")).toHaveText("Home");
  await expect.poll(() => new URL(other.url()).search).toBe("");
  await other.context().close();
});
