//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// IsPidAlive 存活探测：signal 0；EPERM 视为存活（进程存在但无权限）。
func IsPidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil
}

// SpawnDetached 分离式启动本 exe 的 daemon 子命令（新进程组，脱离父进程）。
func SpawnDetached(homeDir string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("解析自身可执行文件失败: %w", err)
	}
	cmd := exec.Command(exe, "daemon")
	if homeDir != "" {
		cmd.Args = append(cmd.Args, "--home", homeDir)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn 失败: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// KillTree 杀进程树：向进程组发 SIGTERM；组不存在则只杀主进程。
func KillTree(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if err2 := syscall.Kill(pid, syscall.SIGTERM); err2 != nil {
			return fmt.Errorf("kill 失败: %w", err2)
		}
	}
	return nil
}
