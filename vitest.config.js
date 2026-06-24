import { defineConfig } from "vitest/config";

// Pure-logic unit tests run in the Node environment (Web Crypto is built in),
// so no browser/jsdom is needed and the suite stays fast and deterministic.
export default defineConfig({
  test: {
    environment: "node",
    include: ["test/**/*.test.js"],
  },
});
