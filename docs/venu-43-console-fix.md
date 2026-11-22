# VENU-43：双击后仍有一个控制台终端 —— 复现、根因与修复

## 现象

双击 `beaver.exe` 后，原生面板窗口正常出现（WebView2 内核，无浏览器地址栏），
但旁边始终挂着**一个 Windows Terminal 标签页**，标题是 exe 路径，内容为 daemon 日志：

```
2026-11-22T06:41:13.677Z [INFO] gateway attached
2026-11-22T06:41:13.683Z [INFO] daemon started at http://127.0.0.1:6280 (home: C:\Users\huang\.beaver)
```

## 复现与证据（修复前 v0.1.1 @1799fad）

| 证据 | 观测值 |
|---|---|
| PE 子系统 | `Subsystem = 3 (WINDOWS_CUI)` —— 控制台程序，Windows 必然分配控制台 |
| 进程树 | 托盘 `beaver.exe`(41084) → `conhost.exe`(44968) 子进程 |
| 控制台宿主 | `WindowsTerminal.exe`(13896)，窗口标题为 `D:\software\ideaIC-projects\beaver\beaver.exe` |
| 关闭 beaver 后 | 该 WindowsTerminal 进程随之消失，证明它只为 beaver 的控制台而存在 |
| 面板本身 | `~/.beaver/logs/panel.log`：`窗口已创建 hwnd=...`，子进程为 `msedgewebview2.exe --embedded-browser-webview=1 --webview-exe-name=beaver.exe` |

结论：改造本身是生效的（原生窗口 + WebView2 嵌入，不是浏览器进程、没有地址栏）；
残留的只有一个**控制台窗口**，用户看到的「cmd 终端」就是它。

## 根因

`cmd/beaver/desktop_cmd.go` 与 `panel_cmd.go` 只做了：

```go
if ownsConsole() { hideConsoleWindow() }   // GetConsoleWindow + ShowWindow(SW_HIDE)
```

默认终端应用是 Windows Terminal 时，控制台走 ConPTY 伪终端，`GetConsoleWindow()`
返回的是伪控制台窗口句柄，`ShowWindow` 无法关闭用户实际看到的那个标签页。
「双击即无黑窗」因此在 Windows 11 + Windows Terminal 环境下不成立。

## 修复

新增 `detachConsole()`（`cmd/beaver/tui_hold_windows.go`）：先 `ShowWindow` 隐藏，
再调用 `kernel32!FreeConsole` 把本进程从控制台解绑。终端会话失去最后一个客户端后
标签页随会话关闭，双击后只剩面板窗口。

- `cmdDesktop` / `cmdPanel` 两个桌面入口都改为调用 `detachConsole()`。
- `beaver tui` / `beaver status` 等命令行路径未改：终端里执行仍有输出。
- 解绑后 stdout 写入失败会被忽略；日志仍落 `~/.beaver/logs/daemon.log`（已验证）。

附带修复两处影响交付核验的缺陷：

1. `internal/daemon/daemon.go`：`Version` 由硬编码 `"0.1.0"` 改为 `version.Value()`。
   面板页头与 `/api/status` 之前恒显示 0.1.0，新旧产物无法区分。
2. `scripts/build-release.ps1`：干净工作区时 `(& git status --porcelain).Trim()`
   对 `$null` 调用方法直接抛错，官方打包脚本必然中断。改为 `("" + (...)).Trim()`。

## 验证

回归脚本 `scripts/verify-native-panel.ps1` 新增「无控制台残留」断言
（按 `tray.pid` 枚举托盘/窗口进程下的 `conhost.exe` / `OpenConsole.exe`），
并在启动前拒绝本机已有 beaver 实例，避免面板按标题全局聚焦导致误判。

```
pwsh -File scripts/verify-native-panel.ps1 -Exe release/v0.1.2/beaver.exe -Port 16473
```

| 场景 | v0.1.1 (1799fad) | v0.1.2 (d5dcd42) |
|---|---|---|
| 首次启动 /api/status 200 | PASS | PASS |
| 原生面板窗口存在 | PASS | PASS |
| 进程模型 托盘 + `__panel` | PASS | PASS |
| **无控制台残留** | **FAIL（conhost 19340）** | **PASS** |
| 二次双击单实例 | PASS | PASS |
| 陈旧 pid + PID 复用 | PASS | PASS |
| daemon 不可达 → 错误页 | PASS | PASS |

同一脚本对旧版报 `FAIL 残留控制台窗口`，对新版通过 —— 断言确实能抓住这个缺陷，
不是空过。

## 产物

| 项 | 值 |
|---|---|
| 源码提交 | `d5dcd426c9a321f...`（分支 `fix/venu-43-console`，父提交 `1799fad`） |
| `beaver.exe version` | `beaver 0.1.2 (d5dcd426c9a3)` |
| exe 大小 / sha256 | 13,677,568 B / `206fbf1b9a0b3fbec11e97149ee963753ad98ea6d5eca1b026204a6a182fb8e8` |
| zip sha256 | `4a0247ae6ef9f9aa9b1f4becfc994c9b208914abbb8f9d2496e2bde7081b161e` |
| 构建方式 | `scripts/build-release.ps1 -Version 0.1.2 -Zip`（脚本已修，不再手工 go build） |
| `go version -m` | `vcs.revision=d5dcd426c9a3221f69ccf13c9b3448822bb6c68d`，`vcs.modified=false` |

## 遗留（未在本分支处理）

- 安装包（Inno/NSIS/winget）、代码签名、macOS/Linux 原生窗口仍未覆盖。
- 面板按窗口标题全局查找：多个 `--home` 实例并存时，`__panel` 会聚焦到已有窗口
  而不是自己的。回归脚本已用前置检查规避，产品侧待定。
- 脚本 `Write-Host` 中文在 Windows PowerShell 5.1 下输出为乱码（仅日志可读性问题）。
