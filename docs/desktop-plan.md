# Beaver 桌面化方案（单文件 exe + 双击壳 + 设置页 + 首启向导）

> 状态：方案已评审，待实施（2026-10-17 定稿；同日复审补充 PoC 清单、遗漏项与任务分解）。
> 不改变产品定位与边界（与 magpie 互补：magpie 管模型，Beaver 管装备），只改变**交付形态**。

## 1. 背景与目标

现状交付形态是 npm 包 + CLI（`dam`）+ 无头 daemon + 浏览器 Web UI，对开发者友好，但有两堵墙：

1. `npm install -g` 要求先有 Node.js/npm（镜像源、PATH 都是隐性认知成本）；
2. 启动与入口需要终端命令，再手动打开 `127.0.0.1:6280`。

**受众校准**：真正的非程序员到不了 MCP 这一层；实际下沉边界是「装了 Cursor/Trae/Windsurf 但不开终端的半技术用户」与「用 Claude Code 但不想碰 npm 的用户」。品类竞品（cc-switch 发安装包、magpie 发签名二进制）均已是安装包交付，npm-only 在品类里是异类。

**目标**：非技术用户全程零终端——下载 exe → 双击 → 托盘常驻 + 面板自动打开 → 向导式配置。开发者路径（npm + CLI）原样保留，降级为快捷通道。

## 2. 总体形态

```
beaver.exe（自包含）
 ├─ daemon（HTTP 6280 + MCP 网关）
 ├─ Web UI 静态资源（现有 bundle 已内嵌 cli/dist/web）
 └─ 托盘 + 启动逻辑
```

双击 → 单实例检测（先探测配置端口 `/api/auth/info` 是否有活 daemon，再查 pid 文件，两者皆无才拉起）→ daemon 拉起 → 托盘图标 → App 窗口打开面板 → 首启进入向导。命令行不再是必需品。

> 只查 pid 文件有竞态：两次快速双击可能各 spawn 一份 daemon，第二份因 EADDRINUSE 漂移到 6281 **并把 6281 持久化进 config**（daemon.ts 现行为），对桌面用户是隐形故障。端口探测 + 壳进程锁文件双保险，该防护对 CLI `dam start` 同样生效。

## 3. M1 单文件 exe

### 3.1 进程模型改造（两条打包路线共通）

- 现状：daemon 入口靠 `require.resolve('@beaver/server')`，`bridge-main.js` 是独立文件——单文件化后两条路都断。
- 改为**进程自拉起**：`dam start` 在 exe 模式下 spawn 自身（`beaver --daemon`），入口检测 `--daemon` 参数直接进 server 主函数；stdio 反向桥同理（`beaver --bridge <url>`）。node 模式行为不变，按运行环境自动选择。
- 同步改造的运行环境判断（抽成可注入的纯函数，便于单测覆盖双态）：
  - `resolveDaemonEntry()` → `resolveDaemonCommand(): { command, args }`；
  - `spawnDetached()` 改用上述结果（exe 模式 spawn 自身，不再拼 `process.execPath + entryFile`）；
  - `autostart.ts` 的 `startCommandLine()` 用 `process.execPath + cliEntry()`（argv[1] 当脚本路径）拼命令——exe 模式没有脚本路径（Bun 编译产物 argv 布局也不同），须按运行态分支为 `"beaver.exe" start`；M3 设置页自启开关直接依赖此处；
  - commander 显式声明 `--daemon` / `--bridge`（隐藏选项），否则未知参数直接报错。
- Web UI 静态资源：`serveStatic()` 现读文件系统（`resolveDefaultWebDist()`），两条打包路线都要加「资产源」抽象——node 态读目录、exe 态读 SEA assets（`node:sea`），路由与 MIME 逻辑不变。

### 3.2 打包路线：Node SEA（定稿，不引入 Bun）

> 决策记录：**不引入 Bun**——「三平台一条命令」的便利抵不上换运行时的全量回归面；SEA 引擎与现有 Node 完全同源，测试结果直接可信。

- 形态：`sea-config.json`（CJS bundle blob + `assets` 内嵌 `dist/web` 与托盘助手二进制）→ `postject` 注入官方 node 二进制。构建机用 Node ≥22（SEA 资产 API 需要），产物自带该 Node 运行时，用户机器无需 Node。
- 代价（相对现有 ESM bundle 的降级项，均已知解法）：
  - 另起一条 esbuild CJS 产物线（`format: 'cjs'`、关 splitting、无 ESM banner/createRequire shim——CJS 有真 require）；bundle 入口链上无 top-level await（`bridge-main.ts` 的 TLA 在 exe 态被 `beaver --bridge` 自拉起取代，不进 bundle），可直接转换；
  - 源码里的 `import.meta.dirname`（process.ts / daemon.ts）在 CJS 输出下会失效——正好都在 §3.1 改造范围内，一并换成基于 `process.execPath` 的运行态判断；
  - Windows 黑窗：node 二进制是 console 子系统，双击必弹黑窗——CI 固定一步 PE 子系统头改 GUI（pkg 时代成熟做法）；exe 定位纯桌面壳（CLI 走 npm 路径），GUI 子系统无副作用。
- 三平台无法交叉编译：GitHub Actions 矩阵在 win/mac/linux runner 各自构建（矩阵本来就有）；macOS postject 注入后需 ad-hoc 重签（`codesign --sign -`）。

PoC 预算半天，跑通「构建 → 双击 → 面板」全链路即定稿。**PoC 必过清单**：

1. PE 子系统改 GUI 后真机验证：双击无黑窗、托盘正常、日志仍落 `~/.beaver/logs/daemon.log`。
2. SEA assets 内嵌 `dist/web`，daemon 正常出 UI（含 SPA 回退路由）。
3. systray2 集成：托盘助手二进制随 assets 内嵌、运行时释放路径正确（node_modules 不会进 exe）。
4. CJS 降级产物过全量测试 + daemon 冒烟（运行时同源，重点验证打包形态而非行为差异）。
5. 真实同步一轮：写 claude-code + cursor + codex 配置（含网关条目与 bridge 条目），回读校验。

### 3.3 体积账（决策记录）

- exe 未压缩 ~90-100MB：自身代码 <1MB，其余为 JS 运行时（引擎 + ICU 国际化数据 ~20MB+）。
- 竞品体积小的原因是语言选型白送的：cc-switch（Tauri）用系统 WebView2 不带引擎；magpie（Go）原生运行时仅 ~2MB。**这是"复用现有 TS 业务逻辑"的固定成本，不是优化差距。**
- 口径对齐：压缩后下载 ~35-45MB，与竞品 50MB 级安装包同量级；磁盘占用诚实写入 README。
- **决策：不为体积改架构**（压缩分发 + 诚实标注）。若未来成为用户投诉，唯一根治路径是 Rust/Go 重写业务层，另行评估。

## 4. M2 双击壳：托盘 + App 窗口

- **托盘**：`systray2`（内嵌小型助手二进制、免编译、跨平台；锁版本控供应链）。菜单五项：打开面板 / 网关启停 / 立即同步 / 开机自启开关 / 退出。**退出 = 停 daemon + 撤托盘；关闭窗口 ≠ 退出。**
- **面板窗口**：优先 Edge `--app`（无地址栏独立窗口，观感同原生应用）→ Chrome `--app` 次选 → 默认浏览器普通标签兜底。封装为 `dam open`，桌面模式与 CLI 共用。实现要点：
  - 端口不写死 6280——从配置与运行态解析（daemon 有端口漂移逻辑）；
  - Windows 经 ShellExecute 启动（`start "" msedge --app=...`），不能 `spawn('msedge')`——后者不解析 App Paths；Edge 在 Win10/11 必在，Chrome 须同时查 HKLM/HKCU App Paths（per-user 安装在 `%LOCALAPPDATA%`，很常见）；
  - 加 `--profile-directory=Default`：多 profile 用户否则会被 Edge 选择器卡住、窗口弹不出来；
  - `dam open` 语义 = **确保 daemon 在跑（未跑则拉起并等待就绪）再开窗**，避免开出一个错误页；桌面双击与 CLI 走同一条路；
  - macOS/Linux 查各自安装路径（Linux 含 chromium 变体）；Firefox 无 `--app`，自然落入默认浏览器兜底。
- **平台**：Windows 首版完整落地（用户与受广大盘）；macOS/Linux 托盘尽力跟进，不阻塞首版。

## 5. M3 设置页（Web UI 新增「设置」tab）

零 JSON 的最后一块：

- **端口**（改后提示重启）· **访问令牌开关** · **开机自启开关**（复用 `dam autostart on/off` 逻辑）· **恢复备份** · **打开数据目录** · **关于**。
- 配套后端：
  - `/api/autostart` GET/PUT——daemon 进程内执行 autostart 逻辑（exe 态入口见 §3.1）；
  - `/api/backups` GET（列 `~/.beaver/backups/<agentType>/`）+ `/api/backups/restore` POST（按 agentType + 备份文件名回写该 agent 当前配置路径）；**恢复后须清除该 agent 的 syncState 哈希**，否则下次同步把回滚内容误判为外部修改、弹冲突；
  - `/api/settings/open-dir` POST（explorer / open / xdg-open）；
  - settings PATCH 扩展 `hasOnboarded`（M4 用）；
  - `config.json` 自身滚动备份（每次保存前，keep=10）——「恢复备份」也能救回管理配置本身，而不只是 agent 配置。
- 「恢复备份」放在显眼位置——治"怕改坏配置"的核心药。
- 「关于」内附卸载说明（退出并撤自启 → 删 exe → 删数据目录）——exe 版无卸载器，把路径写清楚。

## 6. M4 首启向导（Web UI）

- 首启判定：`settings.hasOnboarded` 标记——`Settings` 类型与 store zod schema 新增可选字段（现码库尚无此字段），PATCH 白名单放行。
- 存量用户兼容：字段缺失默认视为「未引导」，但 servers/bindings 非空则视为已引导——npm 老用户升级不被向导绑架，只有全新配置见到向导。
- 三步：**探测**（确认识别的 agent）→ **导入**（复用 import preview）→ **同步**（直连/网关默认策略说明）。
- 每步底部固定安全说明（写前自动备份、可一键恢复）；「跳过，直接进面板」全程可见，不绑架老手。

## 7. M5 分发与文档

- GitHub Actions 三平台构建矩阵 → Releases（`beaver-vX.Y.Z-win-x64.exe` 等），压缩分发；winget 清单后续提报。
- v1 不签名：README 与 Release notes 写明首次运行提示——Windows SmartScreen（「更多信息 → 仍要运行」）、macOS Gatekeeper（右键打开或 `xattr -cr`）。目标用户必踩，不能靠他们自己搜。签名证书后续迭代再上。
- README 双轨：新增「小白安装」截图节（下载 → 双击 → 下一步）；开发者快速开始保持现状。

## 8. 实施任务分解

### 全局约束（每个任务默认继承）

- 引擎门槛 Node ≥20（SEA 构建机 ≥22）；不新增运行时/框架（SEA + React 18 + Tailwind 4 原栈）；
- 监听恒为 127.0.0.1，默认端口 6280，authRequired 默认关；数据目录 ~/.beaver（BEAVER_HOME 覆盖）；
- 开发者路径（npm + dam CLI）行为不得回退；桌面特性全部有 node 态分支；
- UI 文案中文；GitHub Release 发布动作必须由用户手动触发（workflow_dispatch），不自动 publish。

### M1 进程自拉起与 SEA（T1–T6）

**T1 单实例守护 + 拉起改造** — 改 `packages/server/src/process.ts`、`packages/cli/src/index.ts`
- 接口：`resolveDaemonCommand(): { command: string; args: string[] }`（exe 态判定用 `node:sea` `isSea()`，带 try/catch 兜底 false）；`spawnDetached(cmd: { command, args }, homeDir?)`；`isDaemonAlive(base: string): Promise<boolean>`（GET `/api/auth/info`，2s 超时——该端点免鉴权，开了令牌也能探测）；
- `dam start` 改为 pid 检查 → 端口探测 → 都无才拉起；
- 测试 `packages/server/test/process.test.ts`：双态解析、活/死端口探测、并发拉起守卫（注入 fake，不真起进程）。

**T2 autostart 双态** — 改 `packages/cli/src/autostart.ts`；测试 `packages/cli/test/autostart.test.ts`（新）
- `startCommandLine()` 分支：exe 态 `"${process.execPath}" start`；node 态维持现状；钉住含空格/中文路径的引号转义（Windows VBS `""` 转义已有，补用例）。

**T3 资产源抽象** — 改 `packages/server/src/daemon.ts`；测试 `packages/server/test/api.test.ts` 补静态路由用例
- 接口：`AssetSource { read(pathname: string): Promise<Buffer | null> }`；node 态 `FsAssetSource`（行为与现状一致）；SEA 态 `SeaAssetSource`（`node:sea` getRawAsset）；SPA 回退逻辑留在 serveStatic 共用。

**T4 exe 入口分流** — 改 `packages/cli/src/index.ts`、`packages/server/src/index.ts`、`packages/server/src/daemon.ts`
- `server/index.ts` 先去 import 副作用：现入口 import 即执行，包成 `export async function main()`，仅直接运行才 `void main()`；
- CLI 在 commander 之前检查 argv：`--daemon` → `daemonMain()`；`--bridge <url> ...` → `parseBridgeArgs + runBridgeStdio`；两者对 commander 隐藏；
- `daemon.ts` 的 `stdioBridge` 注入按运行态分支：exe 态 `{ command: process.execPath, baseArgs: ['--bridge'] }`；node 态维持 `node bridge-main.js`；
- 测试：argv 分流与 stdioBridge 命令行拼装单测。

**T5 SEA 构建线（PoC）** — 新 `packages/cli/scripts/build-sea.mjs`
- esbuild CJS 产物线（format cjs、关 splitting、无 ESM banner）→ `sea-config.json`（blob + assets：`dist/web` 全量、systray2 托盘助手二进制）→ postject 注入 → Windows PE 子系统改 GUI → macOS `codesign --sign -`；
- 验收 = §3.2 PoC 必过清单 1–5。

**T6 CI 构建矩阵** — 新 `.github/workflows/release.yml`
- win/mac/linux x64 三 runner 各自构建（SEA 不支持交叉编译），产物 `beaver-vX.Y.Z-<platform>-<arch>.<ext>` + zip 压缩；workflow_dispatch 手动触发。

### M2 托盘与面板（T7–T8）

**T7 `dam open`** — 新 `packages/cli/src/open.ts`；测试 `packages/cli/test/open.test.ts`（新）
- 接口：`openPanel(base: string): Promise<void>`——Windows ShellExecute（`start ""`）Edge → Chrome（HKLM/HKCU App Paths）→ 默认浏览器；带 `--profile-directory=Default`；macOS/Linux 按各自路径探测；
- CLI 命令 `dam open`：daemon 未跑 → 拉起并等就绪 → 开窗；超时报错指向 `~/.beaver/logs/daemon.log`；端口从配置/运行态解析。

**T8 桌面壳** — 新 `packages/cli/src/desktop.ts`
- 流程：单实例锁（锁文件 + `isDaemonAlive`）→ 确保 daemon → systray2 托盘（五项菜单）→ `openPanel`；「立即同步」完成后打开面板同步页（v1 无系统通知，结果看日志）；退出 = killTree daemon + 撤托盘；
- exe 无参数双击进 desktop；`--daemon` / `--bridge` / CLI 子命令不受影响。

### M3 设置页与备份（T9–T11）

**T9 设置相关 API** — 改 `packages/server/src/api.ts`、`packages/core/src/types.ts`、`packages/core/src/store.ts`
- GET/PUT `/api/autostart`；POST `/api/settings/open-dir`；GET/PATCH `/api/settings` 增 `hasOnboarded`（Settings 类型 + configSchema 同步）；
- 测试 `packages/server/test/api.test.ts`、`packages/core/test/store.test.ts`。

**T10 备份列表与恢复** — 改 `packages/server/src/api.ts`、`packages/core/src/store.ts`
- GET `/api/backups`（agent 配置备份 + config 备份）；POST `/api/backups/restore`——回写目标 = 该 agentType 适配器当前配置路径（按备份文件名匹配 basename），并**清除该 agent 的 syncState**；
- `Store.save()` 前滚动备份 config.json（backups/config/，keep=10，复用 `backupFile`）；
- 测试：恢复后 syncState 为空、下轮同步不误报冲突；config 损坏后可从备份恢复。

**T11 Web 设置页** — 新 `packages/web/src/pages/SettingsPage.tsx`（App.tsx 挂 tab）
- 端口（改后提示重启）/ 令牌开关 / 自启开关 / 恢复备份（显眼位）/ 打开数据目录 / 关于（卸载说明 + 检查更新跳 Releases）。

### M4 首启向导（T12）

**T12 向导** — 新 `packages/web/src/pages/OnboardingPage.tsx`
- 首启判定：`hasOnboarded` 缺失**且** servers/bindings 为空才进向导（npm 老用户升级不被绑架）；三步复用 `/api/agents`、`/api/import/preview`、`/api/sync`；每步底部安全说明 + 「跳过，直接进面板」；完成写 `hasOnboarded: true`；
- 测试 `packages/core/test/store.test.ts`：派生规则用例。

### M5 分发与文档（T13）

**T13 发布产物 + README 双轨** — 改 `README.md`、`e2e/capture.mjs`
- 产物压缩分发（下载 ~40MB）；README 增「小白安装」截图节（含 SmartScreen「仍要运行」、macOS Gatekeeper 提示）；e2e/capture 补设置页与向导截图；GitHub Release 由用户手动触发。

## 9. 评审关注点

无任务测试直接覆盖、最易咬到真实用户的输入（均已钉到所属任务的用例）：

1. 含空格/中文的安装路径 → autostart 命令行与 VBS 转义（T2）；
2. 端口漂移/改配后 open 与 autostart 仍指向活端口（T1/T7 从运行态解析）；
3. npm 老用户升级后首启不被向导绑架（T12 派生规则）；
4. 双击竞态产生第二 daemon、端口漂移被持久化（T1 探测 + T8 锁文件）；
5. config.json 损坏后桌面用户数据找回（T10 滚动备份 + 恢复接口）。

## 10. 里程碑

| 里程碑 | 内容 | 工作量 |
|---|---|---|
| M1 | T1–T6：进程自拉起 + autostart 双态 + 资产源抽象 + SEA PoC + CI 矩阵 | ~2 天 |
| M2 | T7–T8：`dam open` + 托盘 + App 窗口 | ~1 天 |
| M3 | T9–T11：设置相关 API + 备份恢复 + 设置页 | ~1 天 |
| M4 | T12：首启向导 | ~1 天 |
| M5 | T13：Releases + README 双轨 | ~0.5 天 |

合计约 5.5 天；每个里程碑独立可交付，M1-M4 在 node/npm 模式下先行可用（PoC 清单项除外）。

## 11. 风险与对策

| 风险 | 对策 |
|---|---|
| Windows 双击黑窗 | node 二进制为 console 子系统，CI 固化 PE 子系统头改 GUI；PoC 真机验证 |
| SEA 打包链路（CJS 降级 / 资产注入 / postject） | PoC 半天先跑通全链路；`import.meta` 引用按 §3.1 一并替换 |
| 包体偏大 | 压缩分发（下载 ~40MB）+ README 诚实标注磁盘占用 |
| systray2 二进制依赖 | 锁版本、审计供应链；PoC 验证助手二进制的内嵌/释放路径 |
| 双实例竞态 | 端口 `/api/auth/info` 探测 + pid 文件 + 壳进程锁文件（竞态下第二 daemon 会漂移到 6281 并持久化端口，必须从壳层杜绝） |
| 未签名 exe 首启拦截 | SmartScreen / Gatekeeper 处理方法写进 README 与 Release notes；代码签名后续迭代 |
| 非技术用户更新困难 | v1 不做自动更新，设置页「检查更新」跳转 GitHub Releases，后续迭代 |

## 12. 明确不做

- Electron/Tauri 重写（包括为省体积重写 Rust/Go）；
- Bun 运行时（打包定为 Node SEA：不为「一条命令」的便利引入第二运行时的全量回归成本）；
- 远程多用户（违背纯本机定位）；
- v1 自动更新器。
