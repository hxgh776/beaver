// Package version 单一版本注入点（ldflags -X）。
package version

var (
	value  = "0.1.0"
	commit = ""
)

// String 返回版本号；构建时注入了 commit 则一并给出（交付物血脉可核对）。
func String() string {
	if commit == "" || commit == "unknown" {
		return value
	}
	return value + " (" + commit + ")"
}

// Value 返回纯版本号（不含 commit），供 API / 面板页头展示。
func Value() string { return value }

// Commit 返回构建时注入的 commit（未注入时为空）。
func Commit() string { return commit }