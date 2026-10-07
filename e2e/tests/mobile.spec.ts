// Phone layout checks; runs in the chromium-mobile project only (iPhone 13).
import type { Page } from "@playwright/test";
import { apiUpload, bytes, expect, fileRow, fixture, isChunk, row, test, waitUploaded } from "./helpers";

const LONG_NAME = "an-extremely-long-file-name-without-any-spaces-from-a-camera-export-2026-10-07-0001.jpg";

test.beforeEach(async ({ request }) => {
  // One of each kind of row, with names and text that do not wrap easily.
  await apiUpload(request, "", LONG_NAME, bytes(1000));
  await apiUpload(request, "a-folder-with-a-rather-long-name-too", "x.jpg", "x");
  await apiUpload(request, "", "link.txt", "https://example.com/" + "a".repeat(150));
});

// Compare with clientWidth, not innerWidth: with mobile emulation Chromium
// zooms out to fit content that is too wide, and innerWidth grows with it.
const overflow = (page: Page) => page.evaluate(() => document.scrollingElement!.scrollWidth - document.documentElement.clientWidth);

for (const colorScheme of ["light", "dark"] as const) {
  test(`no horizontal scroll and readable colours (${colorScheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme });
    await page.goto("/");
    await expect(row(page, LONG_NAME)).toBeVisible();
    expect(await overflow(page)).toBeLessThanOrEqual(0);

    const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    const [r, g, b] = bg.match(/\d+/g)!.map(Number);
    if (colorScheme === "dark") expect(r + g + b, bg).toBeLessThan(200);
    else expect(r + g + b, bg).toBeGreaterThan(600);
  });
}

test("tap targets are at least 44 px", async ({ page }) => {
  await page.goto("/");
  await expect(row(page, LONG_NAME)).toBeVisible();
  const small = await page.$$eval(".btn, .icon-btn", (els) =>
    els
      .filter((e) => (e as HTMLElement).offsetParent)
      .filter((e) => {
        const r = e.getBoundingClientRect();
        return r.height < 44 || r.width < 44;
      })
      .map((e) => e.getAttribute("aria-label") || e.textContent),
  );
  expect(small).toEqual([]);
});

test("the add buttons are on screen without scrolling", async ({ page }) => {
  await page.goto("/");
  for (const sel of ["label:has(#pick-files)", "#pick-folder-label", "#text-toggle"]) {
    await expect(page.locator(sel)).toBeInViewport();
  }
});

test("picking photos works, and the upload queue fits the screen", async ({ page }, testInfo) => {
  // Slow the chunks so the queue is on screen with a progress bar.
  await page.route(isChunk, async (route) => {
    await new Promise((r) => setTimeout(r, 300));
    await route.continue();
  });
  await page.goto("/");
  await page.setInputFiles("#pick-files", [
    fixture(testInfo, "IMG_0001.HEIC", bytes(200_000, 1)),
    fixture(testInfo, "another-very-long-name-for-a-video-recorded-on-a-phone-0002.MOV", bytes(300_000, 2)),
  ]);
  await expect(page.locator("#queue")).toBeVisible();
  expect(await overflow(page)).toBeLessThanOrEqual(0);
  await waitUploaded(page, 2);
  await expect(fileRow(page, "IMG_0001.HEIC")).toBeVisible();
  expect(await overflow(page)).toBeLessThanOrEqual(0);
});

test("no horizontal scroll at 360 px wide", async ({ browser, guard }, testInfo) => {
  const ctx = await browser.newContext({ baseURL: testInfo.project.use.baseURL, viewport: { width: 360, height: 740 }, isMobile: true, hasTouch: true });
  const page = await ctx.newPage();
  guard.watch(page);
  await page.goto("/");
  await expect(row(page, LONG_NAME)).toBeVisible();
  await page.click("#text-toggle"); // the composer open as well
  expect(await overflow(page)).toBeLessThanOrEqual(0);
  await ctx.close();
});
