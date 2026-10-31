//go:build !windows

package gateway

import "syscall"

// killProcess 测试用：强杀上游进程模拟崩溃。
func killProcess(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
