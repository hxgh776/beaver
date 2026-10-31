// Package webui 内嵌 Web UI 静态资源。
// 构建脚本把 packages/web/dist 拷贝到本目录后由 go:embed 打进单文件；
// 仓库内提交的是占位 stub，保证裸 `go build` / `go test` 无需 pnpm 即可通过。
package webui

import "embed"

//go:embed all:dist
var Dist embed.FS
