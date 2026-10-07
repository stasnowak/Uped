import { defineConfig, devices } from "@playwright/test";

// End-to-end tests against the real uped server, built from this checkout
// with `go run`. Run with `make e2e` from the repo root, or here with
// `npm ci && npx playwright test`.
//
// Browsers: cloud sessions have Chromium preinstalled under /opt/pw-browsers
// (PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers); never run `playwright
// install` there. CI runs `npx playwright install --with-deps chromium`.
// @playwright/test is pinned to the version matching that Chromium build.

const port = Number(process.env.UPED_E2E_PORT || 18080);
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: "./tests",
  // One shared server whose contents every test resets first (see
  // tests/helpers.ts), so tests run one at a time.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 60_000,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL,
    trace: "retain-on-failure",
  },
  webServer: {
    // The data wipe lives here, not in globalSetup, because Playwright
    // starts the web server before globalSetup runs. The server log goes
    // to test-results/ (which CI uploads on failure) to keep test output
    // readable; 4xx answers are logged as warnings and many tests cause them.
    command:
      "rm -rf .e2e-data && mkdir -p test-results && " +
      `exec go run ../cmd/uped --listen 127.0.0.1:${port} --data ./.e2e-data --ttl 1h --chunk-size 4M 2>test-results/uped-server.log`,
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
    timeout: 180_000, // the first `go run` compiles
    gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 },
  },
  projects: [
    {
      name: "chromium-desktop",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1100, height: 800 } },
      testIgnore: /mobile\.spec\.ts/,
    },
    {
      name: "chromium-mobile",
      // iPhone 13 size, touch and user agent, rendered by Chromium (the only
      // browser installed here); the UA makes the server label it "iPhone Safari".
      use: { ...devices["iPhone 13"], browserName: "chromium" },
      testIgnore: /dragdrop\.spec\.ts/,
    },
  ],
});
