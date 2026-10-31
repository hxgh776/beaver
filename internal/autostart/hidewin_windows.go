//go:build windows

package autostart

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// hideWindow 子进程不弹窗（reg/systemctl 等外部命令调用）。
func HideWindow(c *exec.Cmd) {
	c.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
}
