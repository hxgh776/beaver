# VENU-43 交付记录：本地 exe 按改造后的主干重新构建打包（v0.1.1）

## 结论

用户手上的 `beaver.exe` 之前是 11-18 的 VENU-16 浏览器 `--app` 壳，不是主干的原生窗口版。本次从 `origin/main` `1799fad` 重新构建并打包，产物已在本地仓库落盘，双击即为独立应用窗口（WebView2 原生壳 + 托盘），不再出现 cmd 终端 + 浏览器标签页。

## 产物血脉（四项一致）

| 项 | 值 |
|---|---|
| 源码 commit | `1799fade74fdc6085ea5ea07fd36f50c3ca40a13`（`origin/main`，2026-11-22 12:38 +0800） |
| 工作区状态 | 干净（`vcs.modified=false`） |
| 版本 | `0.1.1` |
| `beaver.exe version` | `beaver 0.1.1 (1799fade74fd)` |
| 单文件 exe | `release/v0.1.1/beaver.exe`，13,677,056 B |
| exe sha256 | `bdd01152c558424602020de02ab14c064893a008891f0e4c2c6c993f5593e74b` |
| 压缩包 | `release/v0.1.1/beaver-v0.1.1-windows-x64-native.zip`，5,325,191 B |
| 压缩包 sha256 | `4d6a5b16e3e5e4b256f587083c3803bc3cae5315a8fbac5a54b9b59294cbef75` |
| 根目录副本 | `beaver.exe`（与 release 内 exe 同 sha256） |
| 旧 exe 备份 | `release/v0.1.1/legacy/beaver-2026-11-18-venu16.exe` |

构建命令（与 `scripts/build-release.ps1` 同参数，见下文「已知缺陷」）:

```
go build -trimpath -buildvcs=true \
  -ldflags "-s -w -X github.com/hxgh776/beaver/internal/version.value=0.1.1 \
            -X github.com/hxgh776/beaver/internal/version.commit=1799fade74fd" \
  -o release/v0.1.1/beaver.exe ./cmd/beaver
```

## 验证（本机实跑）

`scripts/verify-native-panel.ps1 -Exe release\v0.1.1\beaver.exe -Port 16397 -Screenshot` → `RESULT: PASS`（4 场景全过）：

- 首启 `/api/status` 200，`commit=1799fade74fd`；窗口标题 `Beaver（河狸）`；进程模型 = 托盘 + `__panel`（2 个）
- 二次双击单实例，不新开进程
- 陈旧 pid + PID 复用后仍正常起 daemon，`panel.log` 有判定记录
- daemon 不可达时生成错误页，不再白屏

产物标记对比（证明是原生壳而非旧浏览器壳）：

| 标记 | 新 exe | 旧 exe |
|---|---|---|
| `github.com/jchv/go-webview2` 依赖 | 有 | 无 |
| `__panel` 子命令 | 有 | 无 |
| `webview2` 字符串 | 13 处 | 0 |
| 内嵌前端（288 KB bundle，含首启向导「跳过，直接进面板」） | 有（`/` 返回 824 B HTML + `assets/index-CaohkujS.js`） | 无 |

## 已知缺陷（未修，交实现负责人）

1. `scripts/build-release.ps1:21`：干净工作区时 `git status --porcelain` 输出为空 → `(& git status --porcelain).Trim()` 对 `$null` 调方法报错，脚本直接退出，**任何人拉主干后跑官方构建脚本都会失败**。修法：`$dirty = (& git status --porcelain) -join ""` 或 `("" + (& git status --porcelain)).Trim()`。
2. `internal/daemon/daemon.go:30` 的 `const Version = "0.1.0"` 是硬编码，未接 `internal/version`，所以 `/api/status` 的 `version` 恒为 `0.1.0`（同一次构建 `beaver.exe version` 报 `0.1.1`）。血脉核对请以 `beaver.exe version` 与 `go version -m` 的 `vcs.revision` 为准。
3. Windows 版本资源（FileVersion）仍未注入，资源管理器属性里看不到版本，属既有状态。

## 未覆盖

安装包（Inno/NSIS/MSIX/winget）、代码签名、macOS/Linux 原生壳。
