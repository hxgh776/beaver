//go:build !windows

package main

// holdTuiFailureWindow 非 Windows 的终端窗口不随进程关闭，无需驻留；空实现统一调用点。
func holdTuiFailureWindow() {}