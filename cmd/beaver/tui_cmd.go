package main

// beaver tui / 无参数启动：直接进入终端面板（VENU-16）。
// 双模式与 tray 一致：daemon 未跑 = 自持有（退出即停）；已跑 = 挂远端（退出保留）。
// BEAVER_TUI_SNAPSHOT=<page|all> 环境变量 = 无头快照（渲染后输出屏幕文本退出），
// 供验收证明「界面确实渲染」与 CI 冒烟。

import (
	"os"

	"github.com/hxgh776/beaver/internal/tui"
)

func cmdTui() error {
	// --home/--agent-home/--port 由入口层 extractGlobalFlags 统一摘入全局变量
	// （无参数、`beaver tui`、flag 打头三条入口路径行为一致）。
	opts := tui.Options{HomeDir: homeOverride, AgentHome: agentHomeOverride, Port: portOverride}
	if snap := os.Getenv("BEAVER_TUI_SNAPSHOT"); snap != "" {
		opts.Snapshot = snap
		opts.SyncUI = true
	}
	return tui.Run(opts)
}
