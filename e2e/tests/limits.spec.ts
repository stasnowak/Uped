// Each group starts its own uped with a limit (see startServer in helpers.ts).
import { bytes, expect, fileRow, fixture, startServer, test, type ExtraServer, type Listing } from "./helpers";

test.describe("with --max-file-size 1M", () => {
  let srv: ExtraServer;
  test.beforeAll(async () => {
    srv = await startServer(["--max-file-size", "1M"]);
  });
  test.afterAll(async () => {
    await srv?.stop();
  });

  test("a file over the limit shows the server's message and the rest carry on", async ({ page, guard }, testInfo) => {
    guard.allow(/status of 413/);
    const config = await (await page.request.get(srv.url + "/api/config")).json();
    expect(config.maxFileSize).toBe(1 << 20);

    await page.goto(srv.url + "/");
    await page.setInputFiles("#pick-files", [fixture(testInfo, "big.bin", bytes(2 << 20)), fixture(testInfo, "small.txt", "fits")]);
    const big = page.locator("#queue .qitem", { hasText: "big.bin" });
    await expect(big).toHaveAttribute("data-state", "error");
    await expect(big.locator(".qstatus")).toHaveText("file is larger than the server allows");
    await expect(big.locator('button[title="Retry"]')).toBeVisible();
    await expect(page.locator("#queue .qitem", { hasText: "small.txt" })).toHaveAttribute("data-state", "done");
    await expect(fileRow(page, "small.txt")).toBeVisible();
    await expect(page.locator("#queue-summary")).toHaveText("1 file uploaded · 1 need attention");
    await expect(page.locator("#banner")).toBeHidden();
  });
});

test.describe("with --min-free 1000T", () => {
  let srv: ExtraServer;
  test.beforeAll(async () => {
    srv = await startServer(["--min-free", "1000T"]);
  });
  test.afterAll(async () => {
    await srv?.stop();
  });

  test("a full disk pauses the whole queue behind a banner", async ({ page, guard }, testInfo) => {
    guard.allow(/status of 507/);
    await page.goto(srv.url + "/");
    await page.setInputFiles("#pick-files", [fixture(testInfo, "one.txt", "1"), fixture(testInfo, "two.txt", "2")]);

    const banner = page.locator("#banner");
    await expect(banner).toBeVisible();
    await expect(banner).toHaveText(/^Disk full on the server: uploads are paused\./);
    const one = page.locator("#queue .qitem", { hasText: "one.txt" });
    await expect(one).toHaveAttribute("data-state", "paused");
    await expect(one.locator(".qstatus")).toHaveText("not enough free disk space on the server");
    // The next file waits instead of failing too.
    await expect(page.locator("#queue .qitem", { hasText: "two.txt" })).toHaveAttribute("data-state", "queued");

    // Retry asks the server again; the disk is still "full", so the banner returns.
    const refused = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/uploads" && r.status() === 507);
    await one.locator('button[title="Retry"]').click();
    await refused;
    await expect(banner).toBeVisible();
    await expect(one).toHaveAttribute("data-state", "paused");

    const list: Listing = await (await page.request.get(srv.url + "/api/list")).json();
    expect(list.entries).toEqual([]);
    expect(list.uploads).toEqual([]);
  });
});
