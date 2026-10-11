import type { NextConfig } from "next";

// Rewrites are resolved at build time, so API_URL must be set when running
// `next build` (the Dockerfile takes it as a build arg).
const apiURL = process.env.API_URL ?? "http://localhost:8080";

const nextConfig: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
  async rewrites() {
    return [{ source: "/api/:path*", destination: `${apiURL}/api/:path*` }];
  },
};

export default nextConfig;
