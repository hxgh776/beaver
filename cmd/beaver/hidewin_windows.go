//go:build windows

package main

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// hideWindow 子进程不弹窗（reg/systemctl 等外部命令调用）。
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
}
