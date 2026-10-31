//go:build !windows

package autostart

import "os/exec"

// hideWindow 非 Windows 无窗口概念，占位。
func HideWindow(c *exec.Cmd) {}
