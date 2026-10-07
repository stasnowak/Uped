import fs from "node:fs";
import { apiList, apiUpload, dirRow, expect, fixtureTree, metaRe, namesIn, row, test, waitUploaded, type Tree } from "./helpers";

// Two levels of nesting under the picked folder.
const TRIP: Tree = {
  "top.jpg": "one",
  day1: {
    "b.jpg": "two",
    night: { "c.jpg": "three", "d.jpg": "four" },
  },
};

test("Add folder opens a directory chooser", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#pick-folder-label")).toBeVisible();
  const [chooser] = await Promise.all([page.waitForEvent("filechooser"), page.click("#pick-folder-label")]);
  expect(chooser.isMultiple()).toBe(true);
  expect(await chooser.element().evaluate((el) => (el as HTMLInputElement).webkitdirectory)).toBe(true);
});

test("a picked folder keeps its tree on the server", async ({ page, request }, testInfo) => {
  await page.goto("/");
  await page.setInputFiles("#pick-folder", fixtureTree(testInfo, "trip", TRIP));
  await expect(dirRow(page, "trip").locator(".meta")).toHaveText(metaRe("^4 files · 15 B · "), { timeout: 10_000 });
  await waitUploaded(page, 4);

  expect(await namesIn(request, "")).toEqual(["trip"]);
  expect(await namesIn(request, "trip")).toEqual(["day1", "top.jpg"]);
  expect(await namesIn(request, "trip/day1")).toEqual(["b.jpg", "night"]);
  expect(await namesIn(request, "trip/day1/night")).toEqual(["c.jpg", "d.jpg"]);
  const day1 = (await apiList(request, "trip")).entries.find((e) => e.name === "day1")!;
  expect(day1).toMatchObject({ type: "dir", items: 3, size: 12 });
});

test("a folder picked inside a subfolder lands under it", async ({ page, request }, testInfo) => {
  await apiUpload(request, "inbox", "keep.txt", "k");
  await page.goto("/?path=inbox");
  await expect(row(page, "keep.txt")).toBeVisible();
  await page.setInputFiles("#pick-folder", fixtureTree(testInfo, "trip", TRIP));
  await expect(dirRow(page, "trip")).toBeVisible();
  await waitUploaded(page, 4);
  expect(await namesIn(request, "inbox/trip/day1/night")).toEqual(["c.jpg", "d.jpg"]);
  expect(await namesIn(request, "")).toEqual(["inbox"]);
});

test("folder zip: API stream and the row's zip link", async ({ page, request }) => {
  await apiUpload(request, "trip", "top.jpg", "one");
  await apiUpload(request, "trip/day1", "b.jpg", "two");
  await apiUpload(request, "trip/day1/night", "c.jpg", "three");

  const res = await request.get("/api/zip?path=trip");
  expect(res.status()).toBe(200);
  expect(res.headers()["content-type"]).toBe("application/zip");
  expect(res.headers()["content-disposition"]).toMatch(/^attachment; filename="uped-trip-\d{8}-\d{4}\.zip"/);
  const zip = await res.body();
  expect(zip.subarray(0, 4).toString("latin1")).toBe("PK\x03\x04");
  expect(zip.length).toBeGreaterThan(200);
  // Entries are stored uncompressed under the folder's own name.
  for (const name of ["trip/top.jpg", "trip/day1/b.jpg", "trip/day1/night/c.jpg"]) {
    expect(zip.includes(Buffer.from(name)), name).toBe(true);
  }
  expect(zip.includes(Buffer.from("three"))).toBe(true);

  // The zip button on the folder row downloads the same archive.
  await page.goto("/");
  const [dl] = await Promise.all([page.waitForEvent("download"), dirRow(page, "trip").locator('a[title="Download as zip"]').click()]);
  expect(dl.suggestedFilename()).toMatch(/^uped-trip-\d{8}-\d{4}\.zip$/);
  const saved = fs.readFileSync(await dl.path());
  expect(saved.subarray(0, 4).toString("latin1")).toBe("PK\x03\x04");
  expect(saved.length).toBe(zip.length);

  // "Download all" at the root zips everything without a prefix folder.
  await expect(page.locator("#zip-all")).toHaveAttribute("href", "/api/zip");
  const all = await (await request.get("/api/zip")).body();
  expect(all.includes(Buffer.from("trip/day1/night/c.jpg"))).toBe(true);
});

test("deleting a folder removes everything in it", async ({ page, request }) => {
  await apiUpload(request, "trip", "top.jpg", "one");
  await apiUpload(request, "trip/day1/night", "c.jpg", "three");
  await apiUpload(request, "", "stay.txt", "stays");

  await page.goto("/");
  await dirRow(page, "trip").locator('button[title="Delete folder"]').click();
  await expect(dirRow(page, "trip")).toHaveCount(0);
  await expect(page.locator("#toast")).toHaveText("Deleted trip");
  expect(await namesIn(request, "")).toEqual(["stay.txt"]);
  expect(await namesIn(request, "trip")).toBeNull();
  expect(await namesIn(request, "trip/day1/night")).toBeNull();
});
