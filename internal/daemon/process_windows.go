//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

// IsPidAlive 存活探测：OpenProcess 成功即视为存活（被拒绝访问也说明进程存在）。
func IsPidAlive(pid int) bool {
	const processQueryLimitedInformation = 0x1000
	h, err := windows.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}

// SpawnDetached 分离式启动本 exe 的 daemon 子命令：脱离父进程生命周期，隐藏窗口。
// 返回子进程 PID。
func SpawnDetached(homeDir string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("解析自身可执行文件失败: %w", err)
	}
	cmd := exec.Command(exe, "daemon")
	if homeDir != "" {
		cmd.Args = append(cmd.Args, "--home", homeDir)
	}
	cmd.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	// 环境变量传递重定向目录（daemon 子命令再从 flag 读，双保险）
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn 失败: %w", err)
	}
	go func() { _ = cmd.Wait() }() // 回收，避免僵尸
	return cmd.Process.Pid, nil
}

// KillTree 杀进程树：Windows 用 taskkill /T（父子进程链）。
func KillTree(pid int) error {
	out, err := exec.Command("taskkill", "/PID", fmt.Sprint(pid), "/T", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("taskkill 失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
