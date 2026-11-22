package main

// 内部子命令 `__panel`：由托盘 / CLI 分离启动，一个进程只承载一个原生面板窗口。
// 窗口关闭 = 进程退出；托盘、daemon、CLI 均不受影响。

import (
	"os"

	"github.com/hxgh776/beaver/internal/desktop"
	"github.com/hxgh776/beaver/internal/store"
)

func cmdPanel() error {
	if ownsConsole() {
		hideConsoleWindow()
	}
	// 父进程已按实际绑定端口探测过 daemon，优先用它；直接运行（无环境变量）时自行探测。
	base := os.Getenv("BEAVER_PANEL_URL")
	if base == "" {
		b, err := desktop.EnsureRunning(homeOverride, portOverride, agentHomeOverride)
		if err != nil {
			return err
		}
		base = b
	}
	if desktop.FocusPanel() {
		return nil
	}
	return desktop.ShowPanel(store.NewStore(homeOverride).HomeDir, base)
}
