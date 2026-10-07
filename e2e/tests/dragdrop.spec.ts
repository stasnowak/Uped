// Desktop only (playwright.config.ts): phones have no drag and drop.
import { apiUpload, dirRow, dragEnter, dropFiles, dropFolder, expect, fileRow, metaRe, namesIn, row, test, waitUploaded } from "./helpers";

test("the overlay shows while dragging and names the target folder", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#drop")).toBeHidden();
  await dragEnter(page);
  await expect(page.locator("#drop")).toBeVisible();
  await expect(page.locator("#drop-target")).toHaveText("Home");
  await dropFiles(page, [{ name: "x.txt", content: "x" }]);
  await expect(page.locator("#drop")).toBeHidden();
  await expect(fileRow(page, "x.txt")).toBeVisible();
});

test("two dropped files land in the current folder", async ({ page, request }) => {
  await page.goto("/");
  await dropFiles(page, [
    { name: "drop1.txt", content: "dropped one", type: "text/plain" },
    { name: "drop2.txt", content: "dropped two", type: "text/plain" },
  ]);
  await expect(fileRow(page, "drop1.txt")).toBeVisible();
  await expect(fileRow(page, "drop2.txt")).toBeVisible();
  await waitUploaded(page, 2);
  await expect(page.locator("#drop")).toBeHidden();
  expect(await namesIn(request, "")).toEqual(["drop1.txt", "drop2.txt"]);
  await expect(fileRow(page, "drop2.txt").locator(".preview")).toHaveText("dropped two");
});

test("a drop while viewing a subfolder lands in that subfolder", async ({ page, request }) => {
  await apiUpload(request, "inbox", "existing.txt", "hi");
  await page.goto("/?path=inbox");
  await expect(row(page, "existing.txt")).toBeVisible();
  await dragEnter(page);
  await expect(page.locator("#drop-target")).toHaveText("inbox");
  await dropFiles(page, [{ name: "here.txt", content: "in the subfolder" }]);
  await expect(fileRow(page, "here.txt")).toBeVisible();
  expect(await namesIn(request, "inbox")).toEqual(["existing.txt", "here.txt"]);
  expect(await namesIn(request, "")).toEqual(["inbox"]);
});

test("a dropped folder keeps its structure across readEntries batches", async ({ page, request }) => {
  await page.goto("/");
  // Five children read two at a time, plus a nested folder.
  await dropFolder(page, "album", {
    "p1.jpg": "1",
    "p2.jpg": "2",
    "p3.jpg": "3",
    raw: { "p1.dng": "raw1", "p2.dng": "raw2", "p3.dng": "raw3" },
    "notes.txt": "trip notes",
  });
  await expect(dirRow(page, "album").locator(".meta")).toHaveText(metaRe("^7 files · "), { timeout: 10_000 });
  await waitUploaded(page, 7);
  expect(await namesIn(request, "album")).toEqual(["notes.txt", "p1.jpg", "p2.jpg", "p3.jpg", "raw"]);
  expect(await namesIn(request, "album/raw")).toEqual(["p1.dng", "p2.dng", "p3.dng"]);
  // The queue names files by their folder.
  await expect(page.locator("#queue .qname", { hasText: "album/raw/p2.dng" })).toHaveCount(1);
});

test("a folder dropped inside a subfolder nests under it", async ({ page, request }) => {
  await apiUpload(request, "shared", "readme.txt", "x");
  await page.goto("/?path=shared");
  await expect(row(page, "readme.txt")).toBeVisible();
  await dropFolder(page, "scans", { "s1.pdf": "1", deeper: { "s2.pdf": "2" } });
  await expect(dirRow(page, "scans")).toBeVisible();
  await waitUploaded(page, 2);
  expect(await namesIn(request, "shared/scans")).toEqual(["deeper", "s1.pdf"]);
  expect(await namesIn(request, "shared/scans/deeper")).toEqual(["s2.pdf"]);
});
