import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development the three backends are proxied under one origin, exactly as
// nginx does in the container (deploy/web/nginx.conf).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/graphql": "http://localhost:8081",
      "/api/ingest": { target: "http://localhost:8080", rewrite: (p) => p.replace(/^\/api\/ingest/, "") },
      "/api/rag": { target: "http://localhost:8000", rewrite: (p) => p.replace(/^\/api\/rag/, "") },
    },
  },
});
