package main

// beaver tui / 无参数启动：直接进入终端面板（VENU-16）。
// 双模式与 tray 一致：daemon 未跑 = 自持有（退出即停）；已跑 = 挂远端（退出保留）。
// BEAVER_TUI_SNAPSHOT=<page|all> 环境变量 = 无头快照（渲染后输出屏幕文本退出），
// 供验收证明「界面确实渲染」与 CI 冒烟。

import (
	"os"
	"strconv"

	"github.com/hxgh776/beaver/internal/tui"
)

func cmdTui(rest []string) error {
	opts := tui.Options{HomeDir: homeOverride}
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--agent-home":
			if i+1 < len(rest) {
				opts.AgentHome = rest[i+1]
				i++
			}
		case "--port":
			if i+1 < len(rest) {
				if p, err := strconv.Atoi(rest[i+1]); err == nil {
					opts.Port = &p
				}
				i++
			}
		}
	}
	if snap := os.Getenv("BEAVER_TUI_SNAPSHOT"); snap != "" {
		opts.Snapshot = snap
		opts.SyncUI = true
	}
	return tui.Run(opts)
}
