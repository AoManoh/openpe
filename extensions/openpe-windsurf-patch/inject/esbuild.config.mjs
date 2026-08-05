// esbuild-wasm 打包配置：src/index.ts → dist/inject.js（单文件 IIFE）。
// WASM 版本避免在 Windows 与 WSL 共享工作树时误用另一平台的 native
// optional binary；这个 payload 很小，性能差异可接受。
//
// 输出由 Python installer 原样写入 OPENPE-INJECT marker，必须保持：
//   * 单一自包含 IIFE（不残留 import）；
//   * 不向 window/globalThis 暴露可能与宿主冲突的名称；
//   * 除启动 IIFE 外没有顶层副作用。

import { build } from "esbuild-wasm";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const __dirname = dirname(fileURLToPath(import.meta.url));
const outFile = resolve(__dirname, "dist", "inject.js");

await build({
  entryPoints: [resolve(__dirname, "src", "index.ts")],
  outfile: outFile,
  bundle: true,
  format: "iife",
  platform: "browser",
  target: ["es2020"],
  minify: false, // keep readable so users can audit the injected payload
  sourcemap: false,
  legalComments: "inline",
  banner: {
    js: "/* openPE profile-gated IDE inject payload — see extensions/openpe-windsurf-patch/inject/src for sources */",
  },
});

console.log("openpe-ide-inject: built", outFile);
