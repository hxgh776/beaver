//go:build windows

package autostart

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// HideWindow 子进程不弹窗（reg/taskkill/npx 等控制台子进程）。
// 父进程（daemon）是 DETACHED_PROCESS，自身无控制台；仅 HideWindow 挡不住
// npx.cmd 这类 cmd.exe 包装层，需叠加 CREATE_NO_WINDOW 才不新建控制台。
func HideWindow(c *exec.Cmd) {
	c.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
