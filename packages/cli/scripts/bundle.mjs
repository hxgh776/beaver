// 发布打包：把 CLI + server + core + 全部依赖打成零依赖单包。
// 产物：dist/index.js（CLI）、dist/daemon.js（daemon 入口，供 start 分离拉起）、
//       dist/web/（Web UI 静态资源）、共享 chunk（splitting）。
import { build } from 'esbuild';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const cliDir = path.resolve(here, '..');
const webDist = path.resolve(cliDir, '..', 'web', 'dist');

// esbuild 不清空输出目录，先清掉旧 chunk 防止陈旧产物进发布包
await fs.rm(path.resolve(cliDir, 'dist'), { recursive: true, force: true });

const common = {
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node20',
  splitting: true,
  sourcemap: false,
  logLevel: 'info',
  // 只外置 Node 内建模块；@beaver/* 与其余依赖全部内联
  external: ['node:*'],
  // 工作区包映射到源码（tsconfig.bundle.json 的 paths）——内联 dist 产物会自带
  // `import { createRequire } from "node:module"`，与 banner 注入的绑定重复声明
  tsconfig: path.resolve(cliDir, 'tsconfig.bundle.json'),
};

await build({
  ...common,
  entryPoints: [
    { in: path.resolve(cliDir, 'src/index.ts'), out: 'index' },
    { in: path.resolve(cliDir, '../server/src/index.ts'), out: 'daemon' },
  ],
  outdir: path.resolve(cliDir, 'dist'),
  // 源入口不带 shebang；由 banner 统一提供，并注入 createRequire shim
  // （CJS 依赖如 cross-spawn 在 ESM 输出里做动态 require，需要真 require 兜底）。
  // 绑定名用 __createRequire：源码（process.ts 等）也会 import { createRequire }，
  // 同名会触发 ESM 重复声明 SyntaxError。
  banner: {
    js: "#!/usr/bin/env node\nimport { createRequire as __createRequire } from 'node:module';\nconst require = __createRequire(import.meta.url);",
  },
});

await fs.cp(webDist, path.resolve(cliDir, 'dist', 'web'), { recursive: true });

console.log('bundle done: dist/index.js + dist/daemon.js + dist/web');
