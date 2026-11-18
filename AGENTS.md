# AGENTS.md

本文件是 agent / 多会话协作时必须遵守的项目规则。

> 交互入口是**终端面板 TUI**（`beaver` 无参数进入）；浏览器 Web 面板已于 VENU-24 下线，仓库不再有 `packages/web` / `internal/webui` / Node 构建链。

## 规则：用 git worktree 隔离并行任务

本项目可能同时有多个任务并行（多个 agent 会话、多个人工任务）。**每个进行中的任务必须使用独立的 git worktree，禁止在同一次 checkout 上通过切换分支来交错处理多个任务**，否则未提交改动会互相污染、构建产物互相覆盖。

若当前 harness 提供原生 worktree 机制（如 EnterWorktree、/worktree 命令），优先用原生机制；否则按下面流程手动管理。

### 何时必须建 worktree

- 主工作区已有进行中的改动，又要开始另一个任务。
- 多个任务需要并行推进（每个任务一个 worktree）。
- 例外：主工作区干净、改动只有一两处的小修（改个文案、纯文档），可直接在当前 checkout 做。

### 位置与命名

- 统一放在仓库根目录的 `.worktrees/<分支名>/`（`.worktrees/` 已在 .gitignore 忽略，勿提交）。
- 目录名 = 分支名，如 `feat/worktree-isolation`。

### 创建流程

1. **先检测是否已在隔离工作区**，避免嵌套创建（在 submodule 中此判断会误报，需先排除）：

   ```bash
   git rev-parse --show-superproject-working-tree  # 有输出 = 在 submodule，按普通仓库处理
   [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ] && echo already-in-worktree
   ```

   已在 worktree 中则直接使用，不要再新建。

2. 从 main 创建并进入：

   ```bash
   git worktree add .worktrees/feat-xxx -b feat-xxx main
   cd .worktrees/feat-xxx
   ```

3. **每个 worktree 必须单独构建**（Go 构建缓存与单文件产物不跨 worktree 共享）：

   ```bash
   go build -o beaver ./cmd/beaver   # 单文件 CLI + daemon + TUI + 托盘
   ```

4. 跑一次基线测试确认起点干净：`go test ./...`。基线失败先报告，再决定是否继续。

### 运行期隔离（本项目特有，务必遵守）

文件隔离之外，运行时资源不隔离照样互相干扰：

- **端口**：daemon / 网关默认端口 **6280**（`internal/store/store.go`），全局唯一，**用户的真实 Go daemon 正跑在上面**。并行任务起 daemon / TUI 必须用 `--port` 指定各自的端口（其他任务避开），杀进程前必须 `tasklist` 核对镜像名——端口会漂移，PID 会被系统复用，两者都不是身份证明。
- **数据目录**：默认 `~/.beaver` 是用户的全局真实数据（配置、注册表、凭据）。并行任务的 daemon 和集成测试必须用 `--home` 指向临时目录，agent 配置根用 `--agent-home` / 环境变量 `AGENT_CONTROL_AGENT_HOME` 重定向，**禁止写 `~/.beaver` 与真实 agent 配置**。
- **先构建再验证**：daemon 是 Go 单文件，改 Go 码后必须 `go build` 并重启本 worktree 的 daemon，否则验证的是旧代码。

### 收尾

- 任务完成：在 worktree 内提交 → 合并回 main（或发 PR）→ 清理：

  ```bash
  git worktree remove .worktrees/feat-xxx
  git branch -d feat-xxx
  ```

- 废弃的任务同样及时 `git worktree remove`（有未合入改动时先确认再删），不留无主 worktree。
- 偶尔执行 `git worktree prune` 清理失效记录。