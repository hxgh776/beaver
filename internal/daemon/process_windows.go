//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	processQueryLimitedInformation = 0x1000
	processSynchronize             = 0x00100000
)

// IsPidAlive 存活探测：只有进程仍然存在且未退出才算存活。
// OpenProcess 成功仅说明该 pid 当前被某个进程占用（pid 可能已被系统复用），
// 因此还要 WaitForSingleObject(0) 返回 WAIT_TIMEOUT（句柄未 signaled = 未退出）。
func IsPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// SYNCHRONIZE 是 WaitForSingleObject 的必需权限（只给 QUERY_LIMITED 会
	// 返回 ERROR_ACCESS_DENIED，把活进程误判成已退出）。
	h, err := windows.OpenProcess(processQueryLimitedInformation|processSynchronize, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	event, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return event == uint32(windows.WAIT_TIMEOUT)
}

// IsSameExecutable 判断 pid 是否为同一份 beaver 可执行文件。
// pid 文件可能残留（崩溃 / 重启），而该 pid 已被系统复用给无关进程；
// 只按 pid 判活会让托盘认为「已在运行」，从而不再拉起 daemon 并导航到空端口。
func IsSameExecutable(pid int) bool {
	if !IsPidAlive(pid) {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	h, err := windows.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return false
	}
	image := windows.UTF16ToString(buf[:size])
	if image == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(image), filepath.Clean(self))
}

// SpawnDetached 分离式启动本 exe 的 daemon 子命令：脱离父进程生命周期，隐藏窗口。
// portOverride / agentHomeOverride 非空时随 --port / --agent-home 传给子进程。
// 返回子进程 PID。
func SpawnDetached(homeDir string, portOverride *int, agentHomeOverride string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("解析自身可执行文件失败: %w", err)
	}
	cmd := exec.Command(exe, "daemon")
	if homeDir != "" {
		cmd.Args = append(cmd.Args, "--home", homeDir)
	}
	if portOverride != nil {
		cmd.Args = append(cmd.Args, "--port", fmt.Sprint(*portOverride))
	}
	if agentHomeOverride != "" {
		cmd.Args = append(cmd.Args, "--agent-home", agentHomeOverride)
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