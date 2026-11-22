//go:build !windows

package main

// holdTuiFailureWindow 非 Windows 的终端窗口不随进程关闭，无需驻留；空实现统一调用点。
func holdTuiFailureWindow() {}

// hideConsoleWindow 非 Windows 无独立控制台窗口可隐藏；空实现统一调用点。
func hideConsoleWindow() {}

// detachConsole 非 Windows 无控制台可解绑；空实现统一调用点。
func detachConsole() {}

// desktopNotify 非 Windows 无系统消息框；错误仍从 stderr 输出。
func desktopNotify(title, message string) {}