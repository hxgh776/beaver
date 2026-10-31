// 同步 Web UI 构建产物到 Go embed 目录（packages/web/dist → internal/webui/dist）。
// 用法：先 pnpm --filter @beaver/web build，再 node scripts/sync-web-dist.mjs（或 pnpm build:go-web）。
// 对应 TS 侧 esbuild 内嵌 web/dist 的等价物；Go 侧由 internal/webui/embed.go 的 go:embed 消费。
import { cpSync, existsSync, rmSync, statSync } from 'node:fs';
import { resolve } from 'node:path';

const src = resolve(import.meta.dirname, '../packages/web/dist');
const dest = resolve(import.meta.dirname, '../internal/webui/dist');

if (!existsSync(src)) {
  console.error(`✗ 未找到 ${src}，请先执行: pnpm --filter @beaver/web build`);
  process.exit(1);
}
if (existsSync(dest)) {
  const st = statSync(dest);
  if (!st.isDirectory()) {
    console.error(`✗ ${dest} 不是目录（占位 stub 损坏？）`);
    process.exit(1);
  }
  rmSync(dest, { recursive: true, force: true });
}
cpSync(src, dest, { recursive: true });
console.log(`✓ web dist 已同步到 internal/webui/dist（go build 时由 go:embed 打进二进制）`);
