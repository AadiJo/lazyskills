import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = fileURLToPath(new URL(".", import.meta.url));

export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  build: {
    outDir: resolve(here, "../internal/webserver/dist"),
    emptyOutDir: true,
    sourcemap: false,
    target: "es2022"
  },
  server: {
    proxy: {
      "/api": {
        target: process.env.LAZYSKILLS_DEV_SERVER || "http://127.0.0.1:7777",
        changeOrigin: true,
        headers: process.env.LAZYSKILLS_DEV_TOKEN ? { "X-Lazyskills-Token": process.env.LAZYSKILLS_DEV_TOKEN } : undefined,
        configure(proxy) {
          proxy.on("proxyReq", (request) => request.removeHeader("origin"));
        }
      }
    }
  }
});
