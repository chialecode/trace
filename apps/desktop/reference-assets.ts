import { existsSync, readFileSync, realpathSync } from "node:fs";
import { basename, dirname, join, resolve, sep } from "node:path";
import type { Plugin } from "vite";

/** Local design assets are served only by the loopback development server. */
export function referenceAssets(): Plugin {
  return {
    name: "trace-local-reference",
    apply: "serve",
    configureServer(server) {
      const root = resolve(import.meta.dirname, "../..");
      let reference = process.env.TRACE_REFERENCE_DIR;
      if (!reference) {
        let parent = root;
        while (true) {
          const candidate = join(parent, "reference", basename(root));
          if (existsSync(candidate)) {
            reference = candidate;
            break;
          }
          const next = dirname(parent);
          if (next === parent) break;
          parent = next;
        }
      }
      const names = new Set(["portrait", "banner", "feature", "city", "character", "sea"]);
      server.middlewares.use("/__reference-assets", (req, res, next) => {
        const name = req.url?.split("?")[0]?.match(/^\/([a-z]+)\.webp$/)?.[1];
        if (!reference || !name || !names.has(name)) {
          next();
          return;
        }
        const folder = join(reference, "design");
        const file = join(folder, `${name}.webp`);
        if (!existsSync(file)) {
          next();
          return;
        }
        // Reject redirected files outside the explicitly selected design directory.
        if (!realpathSync(file).startsWith(realpathSync(folder) + sep)) {
          next();
          return;
        }
        res.setHeader("Content-Type", "image/webp");
        res.setHeader("Cache-Control", "no-store");
        res.end(readFileSync(file));
      });
    },
  };
}
