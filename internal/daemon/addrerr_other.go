//go:build !windows

package daemon

import (
	"errors"
	"syscall"
)

// isAddrInUse 判定端口占用错误（POSIX: EADDRINUSE）。
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
