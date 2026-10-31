//go:build windows

package daemon

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isAddrInUse 判定端口占用错误（Windows: WSAEADDRINUSE）。
func isAddrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
