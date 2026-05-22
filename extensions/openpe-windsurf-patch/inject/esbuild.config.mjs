// esbuild bundler config: src/index.ts → dist/inject.js (single-file IIFE).
//
// The output is appended verbatim by the Python installer between the
// /* === OPENPE-INJECT-BEGIN === */ markers. It MUST:
//   * be a single self-contained IIFE (no imports left over),
//   * not pollute window / globalThis with names that could clash with
//     Windsurf's bundle,
//   * have no top-level side effects beyond invoking the IIFE.

import { build } from "esbuild";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const __dirname = dirname(fileURLToPath(import.meta.url));
const outFile = resolve(__dirname, "dist", "inject.js");

await build({
  entryPoints: [resolve(__dirname, "src", "index.ts")],
  outfile: outFile,
  bundle: true,
  format: "iife",
  globalName: "openpeInjectIIFE",
  platform: "browser",
  target: ["es2020"],
  minify: false, // keep readable so users can audit the injected payload
  sourcemap: false,
  legalComments: "inline",
  banner: {
    js: "/* openPE Windsurf inject payload — see extensions/openpe-windsurf-patch/inject/src for sources */",
  },
});

console.log("openpe-windsurf-inject: built", outFile);
