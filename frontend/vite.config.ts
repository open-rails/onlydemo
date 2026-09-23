import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

export default defineConfig({
  plugins: [react()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: {
    port: 5173,
    proxy: Object.fromEntries(
      [
        "/api/v1",
        "/auth/v1",
        "/billing/v1",
        "/.well-known",
        "/dev/routes",
        "/health",
      ].map((path) => [
        path,
        {
          target: process.env.DEMO_API_URL || "http://127.0.0.1:3000",
          changeOrigin: false,
        },
      ]),
    ),
  },
  build: { outDir: "dist", sourcemap: true },
});
