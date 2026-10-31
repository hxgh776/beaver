// Package version 单一版本注入点（ldflags -X）。
package version

var value = "0.1.0"

func String() string { return value }
