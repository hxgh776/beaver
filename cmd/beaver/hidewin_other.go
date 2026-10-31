//go:build !windows

package main

import "os/exec"

// hideWindow 非 Windows 无窗口概念，占位。
func hideWindow(c *exec.Cmd) {}
