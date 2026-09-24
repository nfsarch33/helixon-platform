import { defineConfig } from "@playwright/test";

// Layout checks against a RUNNING console (the Go agent serves the static export and the API on one origin), not a
// dev server: overflow depends on real API data such as a 4 KB tool-argument string.
//   CONSOLE_URL=http://127.0.0.1:9410 npm run e2e
export default defineConfig({
  testDir: "./e2e",
  timeout: 60_000,
  retries: 0,
  reporter: [["list"]],
  use: { baseURL: process.env.CONSOLE_URL ?? "http://127.0.0.1:9410", browserName: "chromium", headless: true },
});
