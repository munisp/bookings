import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  reactStrictMode: true,
  async rewrites() {
    // Proxy voice runtime endpoints through the app origin so client code can
    // call relative /voice/* (same-origin, cookies/session preserved).
    const apiBase = process.env.API_BASE_URL ?? "http://localhost:9080";
    return [
      {
        source: "/voice/:path*",
        destination: `${apiBase}/voice/:path*`,
      },
    ];
  },
};

export default nextConfig;
