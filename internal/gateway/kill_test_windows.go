//go:build windows

package gateway

import (
	"fmt"
	"os/exec"
)

// killProcess 测试用：强杀上游进程模拟崩溃。
func killProcess(pid int) {
	_ = exec.Command("taskkill", "/PID", fmt.Sprint(pid), "/F").Run()
}
