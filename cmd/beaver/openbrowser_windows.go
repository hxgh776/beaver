//go:build windows

package main

import (
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"
)

// openBrowser Windows 用 rundll32 FileProtocolHandler（无窗口、免 PowerShell 启动开销）。
func openBrowser(url string) error {
	c := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	c.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	if err := c.Start(); err != nil {
		return fmt.Errorf("打开浏览器失败: %w", err)
	}
	_ = c.Wait()
	return nil
}
