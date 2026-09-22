import { defineConfig } from "vitest/config";
import { fileURLToPath } from "node:url";

export default defineConfig({
  test: {
    environment: "happy-dom",
    include: ["app/**/*.test.ts"],
  },
  resolve: {
    alias: {
      // Nuxt answers both spellings at build time; vitest needs it spelled
      // out to run the same modules outside Nuxt.
      "~": fileURLToPath(new URL("./app", import.meta.url)),
      "@": fileURLToPath(new URL("./app", import.meta.url)),
    },
  },
});
