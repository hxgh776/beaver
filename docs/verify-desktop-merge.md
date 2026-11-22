# 核实：beaver 独立应用改造是否已合入主干（VENU-43）

> 核实时间：2026-11-22 13:45 +0800 · 方式：只读核实（`git fetch origin` 后比对提交树、二进制元数据与 Release 资产）
> 仓库：https://github.com/hxgh776/beaver · 核对时 `origin/main` = `1799fade74fdc6085ea5ea07fd36f50c3ca40a13`（2026-11-22 12:38 +0800）
> 发布位置：分支 `docs/venu-43-verify`（基于 `origin/main` 的 docs-only 提交，可安全合并）· <https://github.com/hxgh776/beaver/blob/docs/venu-43-verify/docs/verify-desktop-merge.md>

## 结论

**已合并。** 桌面独立应用改造（VENU-16 桌面面板 + VENU-27 原生 WebView2 壳）已在 `origin/main` 上。但进入 `main` 的时间是 **2026-11-22 12:36 +0800**（合并提交 `00d4e0d`），不是 11-19。此前 main 一直是 VENU-24 的「仅 TUI」形态，所以用户在此之前看到「终端 cmd + 浏览器」是事实，不是误报。

| 事项 | 结论 | 分支提交 | 进入 main | 关键文件 |
|---|---|---|---|---|
| VENU-16 桌面面板（恢复 React 前端 + 双击开窗） | 已合并 | `5ebcd85` 2026-11-18 21:28 | `866c8e8` → `00d4e0d` | `cmd/beaver/desktop_cmd.go`、`internal/desktop/open.go`、`internal/webui/dist/`、`packages/web/src/` |
| VENU-27 原生 WebView2 面板壳 | 已合并 | `0f5be76` 11-18 22:13、`980dfb8` 11-18 23:59、`a00c1c5` 11-19 00:00 | `866c8e8`（11-19 20:16，任务分支）→ `00d4e0d`（11-22 12:36，main） | `internal/desktop/panel_windows.go`、`panel.go`、`panel_other.go`、`cmd/beaver/panel_cmd.go` |
| 合并记录（VENU-27） | — | `866c8e8` 2026-11-19 20:16 | `00d4e0d` 2026-11-22 12:36 | 父子关系：`00d4e0d = 61ce364 + 866c8e8` |
| VENU-31 Multica MCP/Skill 管理 | 已合并 | `91cde67` | `1799fad` 2026-11-22 12:38 | — |

核对命令与结果：

- `git merge-base --is-ancestor 866c8e8 61ce364` → 否（12:32 的 main 还没有）
- `git merge-base --is-ancestor 866c8e8 00d4e0d` → 是

## 用户为什么仍看到「终端 cmd + 浏览器」

三层原因叠加，任何一层都足以复现旧形态：

1. **主干合入时间晚**：桌面壳直到 2026-11-22 12:36 +0800 才进 `main`；11-19 验收的只是任务分支 `866c8e8`。
2. **本地仓库与产物没跟上**：本地 `main` 停在 `9e498f9`（2026-11-16 22:37，VENU-24 仅 TUI）。
   `git rev-list --left-right --count main...origin/main` → `0	28`（落后 28、领先 0，工作区干净）。
   该提交树里 `internal/desktop/` 只有 `icon.go` 与托盘图标，没有 `panel*.go` / `open.go`；`cmd/beaver/` 没有 `desktop_cmd.go` / `panel_cmd.go`；没有 `internal/webui/dist`、没有 `packages/web/src`。
   无参数启动在该提交直接进 TUI（`cmd/beaver/main.go` 注释与 `cmdTui()` 调用）。
3. **发布产物没跟上**：Releases 只有一个 `v0.1.0`（2026-11-01 12:11 +0800）。Windows 资产实测：
   `go version -m` → `vcs.revision=c1d5cc03ef84dc09c0cf0746ded9bb10df63e181`、`vcs.time=2026-11-01T03:46:10Z`、`vcs.modified=true`；
   无参数运行打印用法并 `exit 2`（该提交无桌面分派），二进制含 `msedge` / `--app=`，无 `__panel` / `panel.log`。
   `.github/workflows/go-build.yml` 仅 `workflow_dispatch` 手动构建、只上传 artifact 不自动 publish；桌面改造后没有新 Release。
   README「快速开始」仍引导用户去 Releases 下载 → 按文档走必然拿到 11-01 的旧形态。
   另：`v0.1.0` 标签指向 `2193f8f`（2026-10-13，TS 时代，无 `cmd/beaver`），与资产报告的 `c1d5cc0` 不一致，发布物无法从标签复现。

## 本地 exe 取证（`D:\software\ideaIC-projects\beaver\beaver.exe`）

- 大小 13,139,968 B，mtime 2026-11-18 21:42:25（比 VENU-16 `5ebcd85` 晚 14 分钟，比 VENU-27 `0f5be76` 早 31 分钟）。
- `beaver.exe version` → `beaver 0.1.0`（无 commit）；`go version -m` 无 vcs 信息，`-ldflags="-s -w"`，`CGO_ENABLED=0`，go1.26.2。
- 依赖含 `github.com/getlantern/systray v1.2.2`，**不含 `github.com/jchv/go-webview2`**。
- 二进制标记：有 `msedge`、`--app=`、`tray.pid`、`index-CaohkujS`；无 `__panel`、`panel.log`、`webview2`。
- ⇒ 这是 **VENU-16 时代（浏览器 `--app` 壳）** 的构建，不是 WebView2 原生壳。
- Windows 版本资源从未注入：`(Get-Item .\beaver.exe).VersionInfo.FileVersion` 为空，不能靠右键属性判新旧。

## 独立应用完成边界（origin/main）

| 项 | 状态 | 证据 / 缺口 |
|---|---|---|
| 托盘常驻 | 完成（Windows） | `cmd/beaver/tray.go`：五项菜单（打开面板 / 网关 / 立即同步 / 开机自启 / 退出）、自持有与远端双模式、`tray.pid` 单实例 |
| WebView2 原生壳 | 完成（Windows） | `internal/desktop/panel_windows.go` 用 `go-webview2`（纯 Go），Loader 内嵌内存加载；`__panel` 子进程承载窗口；标题跨进程聚焦；数据目录 `~/.beaver/webview2`；Runtime 缺失或 `BEAVER_NO_NATIVE_PANEL=1` 回退浏览器 |
| 非 Windows 原生壳 | 未覆盖 | `internal/desktop/panel_other.go` 直接回退浏览器（注释明确「Windows 才是双击 exe 分发的目标平台」） |
| 单文件 exe | 完成 | Go 单二进制 + 前端 `go:embed`（`internal/webui/embed.go`）；`scripts/build-release.ps1` 注入 version+commit、可选 zip 并打印 sha256。缺口：Windows VERSIONINFO 未注入 |
| 首启向导 | 完成 | `packages/web/src/pages/OnboardingPage.tsx`；`hasOnboarded`（`internal/store/store.go:145`、`internal/daemon/api.go`）；判定见 `packages/web/src/App.tsx:96-108`（未引导且注册表为空才进向导）；已打进 `internal/webui/dist`（产物含 `hasOnboarded`、`跳过，直接进面板`），且 src 自 `5ebcd85` 后无漂移 |
| 安装包 | 未覆盖 | 无 Inno/NSIS/MSIX/winget；`go-build.yml` 只产 zip / tar.gz 解压即用 |
| 代码签名 | 未覆盖（明确 v1 不签） | README 写明 SmartScreen「更多信息 → 仍要运行」、macOS Gatekeeper；无签名步骤 |
| macOS / Linux 分发 | 部分 | `go-build.yml` 有 x64 / arm64 构建矩阵产出 tar.gz；无原生壳、无签名/公证 |

文档漂移：`docs/desktop-plan.md` 顶部仍写「方案已评审，待实施（2026-10-17）」，且 M1/M5 段落仍是 TS/npm 方案，与当前 Go 实现不符——不要拿它当状态依据。

## 用户可执行的验证步骤（非程序员版）

全部在 `cmd` 里复制粘贴。

1. 看本地是不是旧提交：

```
cd /d D:\software\ideaIC-projects\beaver
git fetch origin
git log -1 --oneline
git rev-list --left-right --count main...origin/main
```

期望（核实当时）：`9e498f9 chore: 保留 node_modules/dist 忽略规则…`；`0	28`。

2. 更新到主干（本地 main 是 origin/main 的祖先，可快进）：

```
git checkout main
git merge --ff-only origin/main
git log -1 --oneline
```

期望：`1799fad merge: 合入 Multica MCP/Skill 管理（VENU-31 @91cde67，验收通过）`。

3. 构建新 exe（两种方式任选；需要已装 Go）：

```
go build -o beaver-new.exe ./cmd/beaver
```

或（带版本号 / commit 注入，产物在 `release\v0.1.0\`）：

```
pwsh -File scripts/build-release.ps1 -Version 0.1.0 -Zip
```

4. 确认拿到的是原生窗口新版（旧 exe 必然查不到）：

```
.\beaver-new.exe version
go version -m .\beaver-new.exe | findstr /i "webview2 vcs.revision"
```

期望：脚本构建为 `beaver 0.1.0 (<commit>)`；findstr 输出含 `github.com/jchv/go-webview2`。

5. 双击后的观感（与旧版对照）：

- 任务栏是独立应用图标，窗口无地址栏、无标签页；
- 任务管理器出现 `beaver-new.exe __panel` 子进程；
- 首次打开后生成 `%USERPROFILE%\.beaver\logs\panel.log`（旧版没有此文件）；
- 托盘菜单：打开面板 / 网关 / 立即同步 / 开机自启 / 退出。

6. 为什么不能看 FileVersion：`(Get-Item .\beaver-new.exe).VersionInfo.FileVersion` 为空——构建未注入 Windows 版本资源。请用第 4 步的 `go version -m` 与第 5 步的 `panel.log` 判断。

## 限制

- 只读核实，未改源码；未新增 `e2e/` 用例（本 issue 限定改动面 `docs/**`）。
- macOS / Linux 原生壳、安装包、签名：仓库内无实现资料，按「未覆盖」处理，不做推测。
