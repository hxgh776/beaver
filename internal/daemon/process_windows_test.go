//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestIsPidAliveSelf(t *testing.T) {
	if !IsPidAlive(os.Getpid()) {
		t.Fatal("当前进程应被判定为存活")
	}
	if !IsSameExecutable(os.Getpid()) {
		t.Fatal("当前进程应被判定为同一份 beaver 可执行文件")
	}
}

func TestIsPidAliveRejectsExitedProcess(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动辅助进程: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	// Windows 上 PID 立即复用概率极低；Wait 后句柄仍持有，这里按 pid 号探测。
	if IsPidAlive(pid) {
		t.Fatalf("已退出的 pid %d 不应判定为存活", pid)
	}
}

func TestIsSameExecutableRejectsForeignProcess(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "ping", "-n", "10", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动辅助进程: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for !IsPidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !IsPidAlive(pid) {
		t.Skip("辅助进程未能进入运行态")
	}
	if IsSameExecutable(pid) {
		t.Fatalf("cmd.exe (pid %d) 不应被判定为 beaver 进程", pid)
	}
}