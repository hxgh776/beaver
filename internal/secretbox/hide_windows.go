//go:build windows

package secretbox

import (
	"os/exec"
	"syscall"
)

// hideWindow 隐藏 PowerShell 子进程的控制台窗口（对应 TS spawn 的 windowsHide: true）。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
