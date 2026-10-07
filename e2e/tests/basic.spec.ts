import fs from "node:fs";
import { apiList, apiUpload, bytes, deviceLabel, download, expect, fileRow, fixture, metaRe, plainText, row, sha256, test, waitUploaded } from "./helpers";

test("empty state, title and footer", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveTitle("uped");
  await expect(page.locator("#empty")).toBeVisible();
  // The expiry sentence follows the server's --ttl (1h in playwright.config.ts).
  await expect(page.locator("#empty-text")).toHaveText("Drop files here or tap Add files. Everything disappears after 1 hour.");
  await expect(page.locator("#list li")).toHaveCount(0);
  await expect(page.locator("#zip-all")).toBeHidden();
  await expect(page.locator("#queue")).toBeHidden();
  await expect(page.locator("#version")).toHaveText(/^uped \S+$/);
  await expect(page.locator("#crumbs [aria-current]")).toHaveText("Home");
  await expect(page.locator("#free")).toHaveText(/ free$/);
  await expect(page.locator("#live.on")).toHaveCount(1); // the event stream is connected
});

test("Add files opens a multi-select chooser", async ({ page }) => {
  await page.goto("/");
  const [chooser] = await Promise.all([page.waitForEvent("filechooser"), page.click("label:has(#pick-files)")]);
  expect(chooser.isMultiple()).toBe(true);
  await chooser.setFiles([]);
});

test("picked files become rows; download is byte-equal; delete removes the row", async ({ page, request }, testInfo) => {
  const note = fixture(testInfo, "a.txt", "alpha\n");
  const photo = bytes(300_000, 7);
  const photoPath = fixture(testInfo, "photo.jpg", photo);

  await page.goto("/");
  await page.setInputFiles("#pick-files", [note, photoPath]);
  await expect(page.locator("#list li.item.file")).toHaveCount(2, { timeout: 5_000 });
  await waitUploaded(page, 2);
  await expect(page.locator("#queue-summary")).toHaveText("2 files uploaded");
  await expect(page).toHaveTitle("uped"); // back from "NN% · uped" once idle

  // Names, sizes, device, age and expiry on the meta line.
  const device = deviceLabel(testInfo);
  expect(await plainText(fileRow(page, "photo.jpg").locator(".meta"))).toMatch(new RegExp(`^293 KB · ${device} · just now · expires in (59|60) min$`));
  expect(await plainText(fileRow(page, "a.txt").locator(".meta"))).toMatch(/^6 B · /);
  await expect(fileRow(page, "a.txt").locator(".preview")).toHaveText("alpha");
  await expect(page.locator("#zip-all")).toBeVisible();
  await expect(page.locator("#zip-all")).toHaveText("Download all");

  // Real links to /d/ with a download attribute; the bytes match.
  await expect(fileRow(page, "a.txt").locator("a.name")).toHaveAttribute("href", "/d/a.txt");
  await expect(fileRow(page, "a.txt").locator("a.name")).toHaveAttribute("download", "a.txt");
  expect((await download(request, "a.txt")).toString()).toBe("alpha\n");
  expect(sha256(await download(request, "photo.jpg"))).toBe(sha256(photo));

  // The browser's own download of the link gets the same bytes and name.
  const [dl] = await Promise.all([page.waitForEvent("download"), fileRow(page, "photo.jpg").locator('a[title="Download"]').click()]);
  expect(dl.suggestedFilename()).toBe("photo.jpg");
  expect(sha256(fs.readFileSync(await dl.path()))).toBe(sha256(photo));

  await fileRow(page, "photo.jpg").locator('button[title="Delete"]').click();
  await expect(fileRow(page, "photo.jpg")).toHaveCount(0);
  await expect(page.locator("#toast")).toHaveText("Deleted photo.jpg");
  expect((await apiList(request)).entries.map((e) => e.name)).toEqual(["a.txt"]);
});

test("a name already taken gets a (1) suffix", async ({ page, request }, testInfo) => {
  await apiUpload(request, "", "report.pdf", "first");
  await page.goto("/");
  await page.setInputFiles("#pick-files", fixture(testInfo, "report.pdf", "second"));
  await expect(fileRow(page, "report (1).pdf")).toBeVisible();
  // The queue row shows the name the server chose.
  await expect(page.locator('#queue .qitem[data-state="done"] .qname')).toHaveText("report (1).pdf");
  expect((await download(request, "report.pdf")).toString()).toBe("first");
  expect((await download(request, "report (1).pdf")).toString()).toBe("second");
});

test("folders: breadcrumbs, back button, deep links and a missing folder", async ({ page, request, guard }) => {
  await apiUpload(request, "trip", "top.jpg", "one");
  await apiUpload(request, "trip/day1", "b.jpg", "two");

  await page.goto("/");
  await expect(row(page, "trip").locator(".meta")).toHaveText(metaRe("^2 files · 6 B · just now · expires in (59|60) min$"));
  await row(page, "trip").locator("a.name").click();
  await expect(row(page, "day1")).toBeVisible();
  await expect(row(page, "top.jpg")).toBeVisible();
  expect(page.url()).toMatch(/\/\?path=trip$/);
  await expect(page.locator("#crumbs [aria-current]")).toHaveText("trip");
  await expect(page.locator("#zip-all")).toHaveText("Download folder");
  await expect(page.locator("#zip-all")).toHaveAttribute("href", "/api/zip?path=trip");
  // Inside a folder, file links carry the full path.
  await expect(row(page, "top.jpg").locator("a.name")).toHaveAttribute("href", "/d/trip/top.jpg");

  await row(page, "day1").locator("a.name").click();
  await expect(row(page, "b.jpg")).toBeVisible();
  await expect(page.locator("#crumbs")).toHaveText(/Home\s*\/\s*trip\s*\/\s*day1/);

  await page.locator("#crumbs a", { hasText: "trip" }).click();
  await expect(row(page, "day1")).toBeVisible();
  await page.goBack();
  await expect(row(page, "b.jpg")).toBeVisible();
  expect(page.url()).toMatch(/\/\?path=trip%2Fday1$/);
  await page.goBack();
  await page.goBack();
  await expect(row(page, "trip")).toBeVisible();
  expect(new URL(page.url()).search).toBe("");

  // A reload keeps the folder.
  await page.goto("/?path=trip/day1");
  await expect(row(page, "b.jpg")).toBeVisible();
  await page.reload();
  await expect(row(page, "b.jpg")).toBeVisible();
  await page.locator("#home-link").click();
  await expect(row(page, "trip")).toBeVisible();

  // A folder that is gone falls back to Home with a message.
  guard.allow(/\/api\/list\?path=nope/);
  await page.goto("/?path=nope");
  await expect(page.locator("#toast")).toContainText("That folder is gone");
  await expect.poll(() => new URL(page.url()).search).toBe("");
  await expect(row(page, "trip")).toBeVisible();
});
