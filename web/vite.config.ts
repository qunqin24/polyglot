import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

// The build output goes straight into web/dist, where go:embed picks it up.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // A self-hosted admin UI does not need source maps in the binary.
    sourcemap: false,
    rollupOptions: {
      output: {
        // The bundled IANA rules change on a different cadence from the app
        // and are large enough to benefit from their own cacheable chunk.
        manualChunks(id) {
          if (id.includes("/moment/") || id.includes("/moment-timezone/")) return "timezone";
        },
      },
    },
  },
  server: {
    port: 5173,
    // In dev the Go server runs on :3000 and owns the API.
    proxy: {
      "/api": "http://127.0.0.1:3000",
      "/health": "http://127.0.0.1:3000",
    },
  },
});
