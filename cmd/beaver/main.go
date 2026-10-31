// Beaver（河狸）——本地 Agent 装备总控台。
// 本入口在 M4 前只是构建占位：完整 CLI 子命令在 M4 落地（cobra）。
package main

import (
	"fmt"
	"os"

	"github.com/hxgh776/beaver/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("beaver", version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "beaver CLI 尚在 Go 移植中（M4 落地）；当前请使用 npm 版 dam 命令")
	os.Exit(2)
}
