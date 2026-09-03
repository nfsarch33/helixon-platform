import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
    testTimeout: 15_000,
    // enabled here rather than only on the command line: a threshold that
    // depends on someone remembering --coverage enforces nothing, and reads
    // in review as though it does.
    coverage: { enabled: true, provider: "v8", reporter: ["text"], include: ["src/**/*.{ts,tsx}"], exclude: ["src/test/**", "src/**/*.test.{ts,tsx}"], thresholds: { lines: 60 } },
  },
});
