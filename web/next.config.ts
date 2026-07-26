import path from "path";
import { fileURLToPath } from "url";

import type { NextConfig } from "next";

// Pin the project root to this directory. Next infers it by walking up for a
// lockfile, so a stray package-lock.json anywhere above the checkout wins and
// the standalone build gets nested under the path from that root — server.js
// lands at .next/standalone/<path>/web/server.js instead of
// .next/standalone/server.js, and the deployed service dies with
// MODULE_NOT_FOUND.
const projectRoot = path.dirname(fileURLToPath(import.meta.url));

const nextConfig: NextConfig = {
  output: "standalone",
  turbopack: { root: projectRoot },
  outputFileTracingRoot: projectRoot,
};

export default nextConfig;
