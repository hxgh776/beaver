//go:build !windows

package desktop

// 非 Windows 平台暂无无 CGO 的原生壳（Linux 需 GTK/WebKitGTK，macOS 需 Cocoa），
// 保持既有浏览器打开链路；Windows 才是双击 exe 分发的目标平台。

func launchPanelProcess(homeOverride string, portOverride *int, agentHomeOverride, base string) error {
	return OpenAppWindow(base)
}

func ShowPanel(homeDir, base string) error { return OpenAppWindow(base) }

func focusPanelWindow() bool { return false }

func closePanelWindow() {}
