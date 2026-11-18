//go:build !windows

package multica

import "os/exec"

// hideWindow 非 Windows 平台无需处理。
func hideWindow(cmd *exec.Cmd) {}

// hideFile 非 Windows 平台无需处理。
func hideFile(path string) {}
