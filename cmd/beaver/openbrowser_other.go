//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openBrowser macOS 用 open，Linux 用 xdg-open。
func openBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if err := exec.Command(name, url).Start(); err != nil {
		return fmt.Errorf("打开浏览器失败: %w", err)
	}
	return nil
}
