//go:build windows

package multica

import (
	"os/exec"
	"syscall"
)

// hideWindow Windows 下不弹 CMD 窗（daemon 常驻进程的子进程一律隐藏，同 secretbox/网关做法）。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// hideFile Windows 下给临时凭据文件加隐藏属性（best-effort）。
func hideFile(path string) {
	// Go 标准库无直接 API；kernel32 SetFileAttributes 经 syscall 成本高于收益，
	// 0600 权限 + 立即删除已是主防线，这里不再加属性。
	_ = path
}
