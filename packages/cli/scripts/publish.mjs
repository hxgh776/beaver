// 发布脚本：从临时目录发布零依赖单包（不改动工作区 package.json）。
// 用法：npm run release（先完成 tsc 构建）
//  - dist 内联全部依赖，发布清单 dependencies = {}
//  - README / LICENSE 一并带入
import { execSync } from 'node:child_process';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const cliDir = path.resolve(here, '..');
const tmp = path.resolve(cliDir, '.publish-tmp');

await fs.rm(tmp, { recursive: true, force: true });
await fs.mkdir(tmp, { recursive: true });

const pkg = JSON.parse(await fs.readFile(path.resolve(cliDir, 'package.json'), 'utf8'));
const publishManifest = {
  name: pkg.name,
  version: pkg.version,
  description: pkg.description,
  type: 'module',
  bin: pkg.bin,
  files: ['*'],
  engines: pkg.engines,
  license: 'MIT',
  repository: pkg.repository ?? { type: 'git', url: 'git+https://github.com/hxgh776/beaver.git' },
  dependencies: {},
};
await fs.writeFile(path.join(tmp, 'package.json'), JSON.stringify(publishManifest, null, 2) + '\n');

await fs.cp(path.resolve(cliDir, 'dist'), path.join(tmp, 'dist'), { recursive: true });
for (const f of ['README.md', 'LICENSE']) {
  await fs.cp(path.resolve(cliDir, '../..', f), path.join(tmp, f));
}
// README 里的相对路径图片在 npmjs.com 上按包内文件解析，品牌图需随包携带
await fs.cp(path.resolve(cliDir, '../..', 'docs/brand'), path.join(tmp, 'docs/brand'), { recursive: true });

// 发布必须直连官方源：本机全局 registry 常配成 npmmirror 镜像，镜像不接受发布
execSync(`npm publish "${tmp}" --access public --registry=https://registry.npmjs.org`, {
  stdio: 'inherit',
  cwd: cliDir,
});
await fs.rm(tmp, { recursive: true, force: true });
