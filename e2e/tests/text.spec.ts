import type { Page } from "@playwright/test";
import { apiUpload, download, expect, fileRow, namesIn, paste, test } from "./helpers";

// A 1x1 PNG.
const PNG_BASE64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==";

const clipboard = (page: Page) => page.evaluate(() => navigator.clipboard.readText());

/**
 * Makes the page behave as on a plain-HTTP LAN address, where browsers hide
 * the async clipboard API (the tests run on 127.0.0.1, a secure context),
 * and records execCommand calls. With blocked, execCommand("copy") fails.
 */
async function insecureContext(page: Page, blocked = false) {
  await page.addInitScript((blocked) => {
    Object.defineProperty(window, "isSecureContext", { get: () => false });
    const w = window as unknown as { execCalls: string[] };
    w.execCalls = [];
    const exec = document.execCommand.bind(document);
    document.execCommand = (cmd: string, ...rest: never[]) => {
      w.execCalls.push(cmd);
      return blocked ? false : exec(cmd, ...rest);
    };
  }, blocked);
}

test.beforeEach(async ({ context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
});

test("Add text saves a note named after its first line", async ({ page, request }) => {
  await page.goto("/");
  await expect(page.locator("#composer")).toBeHidden();
  await page.click("#text-toggle");
  await expect(page.locator("#composer")).toBeVisible();
  await expect(page.locator("#text-toggle")).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator("#text-input")).toBeFocused();

  await page.fill("#text-input", "Shopping list\nmilk\neggs");
  await page.click("#text-save");
  await expect(fileRow(page, "shopping-list.txt")).toBeVisible();
  await expect(page.locator("#toast")).toHaveText("Saved shopping-list.txt");
  await expect(page.locator("#composer")).toBeHidden();
  expect(await fileRow(page, "shopping-list.txt").locator(".preview").textContent()).toBe("Shopping list\nmilk\neggs");
  expect((await download(request, "shopping-list.txt")).toString()).toBe("Shopping list\nmilk\neggs");
});

test("composer: links, Ctrl+Enter, Escape and empty text", async ({ page, request }) => {
  await page.goto("/");
  await page.click("#text-toggle");
  await page.fill("#text-input", "https://www.example.com/some/page");
  await page.press("#text-input", "Control+Enter");
  await expect(fileRow(page, "link-example.com.txt")).toBeVisible();

  await page.click("#text-toggle");
  await page.fill("#text-input", "   ");
  await page.click("#text-save");
  await expect(page.locator("#toast")).toHaveText("Nothing to save");
  await expect(page.locator("#composer")).toBeVisible();
  await page.press("#text-input", "Escape");
  await expect(page.locator("#composer")).toBeHidden();
  expect(await namesIn(request, "")).toEqual(["link-example.com.txt"]);
});

test("a note is saved into the folder being viewed", async ({ page, request }) => {
  await apiUpload(request, "notes", "old.txt", "old");
  await page.goto("/?path=notes");
  await expect(fileRow(page, "old.txt")).toBeVisible();
  await page.click("#text-toggle");
  await page.fill("#text-input", "Door code 4711");
  await page.click("#text-save");
  await expect(fileRow(page, "door-code-4711.txt")).toBeVisible();
  expect(await namesIn(request, "notes")).toEqual(["door-code-4711.txt", "old.txt"]);
});

test("pasting text outside an input saves it; pasting into the composer does not", async ({ page, request }) => {
  await page.goto("/");
  await page.click("#text-toggle");
  await paste(page, { text: "Typed into the box" }, "#text-input");
  await paste(page, { text: "Pasted note\nsecond line" });
  await expect(fileRow(page, "pasted-note.txt")).toBeVisible();
  expect(await fileRow(page, "pasted-note.txt").locator(".preview").textContent()).toBe("Pasted note\nsecond line");
  expect(await namesIn(request, "")).toEqual(["pasted-note.txt"]);
});

test("pasting an image uploads it as pasted-<time>.png", async ({ page, request }) => {
  await page.goto("/");
  await paste(page, { files: [{ name: "image.png", type: "image/png", base64: PNG_BASE64 }] });
  const pngRow = page.locator("#list li.item.file").filter({ has: page.locator("a.name", { hasText: /^pasted-\d{8}-\d{6}\.png$/ }) });
  await expect(pngRow).toHaveCount(1);
  const name = (await namesIn(request, ""))![0];
  expect(name).toMatch(/^pasted-\d{8}-\d{6}\.png$/);
  expect((await download(request, name)).toString("base64")).toBe(PNG_BASE64);
});

test("text that is too long for a note is refused with a message", async ({ page, request }) => {
  await page.goto("/");
  await paste(page, { text: "x".repeat((1 << 20) + 1) });
  await expect(page.locator("#toast")).toHaveText("That text is too long to save as a note; save it as a file instead.");
  expect(await namesIn(request, "")).toEqual([]);
});

test("Copy puts a note on the clipboard", async ({ page, request }) => {
  await apiUpload(request, "", "a.txt", "alpha\n");
  await page.goto("/");
  await fileRow(page, "a.txt").locator('button[title="Copy text"]').click();
  await expect(page.locator("#toast")).toHaveText("Copied");
  expect(await clipboard(page)).toBe("alpha\n");
});

test("Copy on a plain-HTTP origin falls back to execCommand, also for long notes", async ({ page, request }) => {
  await insecureContext(page);
  // Longer than the 200-character preview, so the app fetches the file first.
  const long = Array.from({ length: 40 }, (_, i) => `line ${i + 1} of a long note`).join("\n");
  await apiUpload(request, "", "long.txt", long);
  await apiUpload(request, "", "short.txt", "short note");
  await page.goto("/");

  await fileRow(page, "short.txt").locator('button[title="Copy text"]').click();
  await expect(page.locator("#toast")).toHaveText("Copied");
  expect(await clipboard(page)).toBe("short note");

  await fileRow(page, "long.txt").locator('button[title="Copy text"]').click();
  await expect.poll(() => clipboard(page)).toBe(long);
  expect(await page.evaluate(() => (window as unknown as { execCalls: string[] }).execCalls)).toEqual(["copy", "copy"]);
});

test("when copying is blocked, a dialog shows the text to copy by hand", async ({ page, request }) => {
  await insecureContext(page, true);
  await apiUpload(request, "", "code.txt", "Wi-Fi password: hunter2");
  await page.goto("/");
  await fileRow(page, "code.txt").locator('button[title="Copy text"]').click();
  await expect(page.locator("#copy-dialog")).toBeVisible();
  await expect(page.locator("#copy-text")).toHaveValue("Wi-Fi password: hunter2");
  await page.locator("#copy-dialog button", { hasText: "Done" }).click();
  await expect(page.locator("#copy-dialog")).toBeHidden();
});
