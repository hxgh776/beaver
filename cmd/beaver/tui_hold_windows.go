//go:build windows

package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// consoleAttached 报告 stdin/stdout 是否都绑定到 Windows 控制台。
// 管道、输出重定向、CI（无控制台）时为 false——此时驻留只会挂住脚本。
func consoleAttached() bool {
	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode); err != nil {
		return false
	}
	if err := windows.GetConsoleMode(windows.Handle(os.Stdout.Fd()), &mode); err != nil {
		return false
	}
	return true
}

// ownsConsole 报告当前控制台是否只挂着本进程。
// 资源管理器双击 exe 时 Windows 会新建一个控制台：进程一退出窗口立刻消失，
// 启动错误根本来不及看。已有终端里启动（shell 与本进程共用控制台）时返回 false。
func ownsConsole() bool {
	var pid uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pid)), 1)
	return n == 1
}

// holdTuiFailureWindow 只在"双击启动"失败时留住窗口，等用户看清错误再关闭；
// 终端里启动、脚本、CI 一律直接返回，不改变退出码也不阻塞调用方。
func holdTuiFailureWindow() {
	if !consoleAttached() || !ownsConsole() {
		return
	}
	fmt.Fprintln(os.Stderr, "\n按任意键关闭窗口……")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}