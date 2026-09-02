import type { NextConfig } from "next";

// The operator console is a static export embedded into the helixon binary
// (docs/adr/0004): pre-rendered HTML per route, served at /console/ next to
// the read API it renders. No image optimisation server exists at runtime.
const nextConfig: NextConfig = {
  output: "export",
  basePath: "/console",
  trailingSlash: true,
  images: { unoptimized: true },
  reactStrictMode: true,
};

export default nextConfig;
