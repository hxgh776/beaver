// Package environment S5 环境检测：探测本机 server 运行时是否可用。
// 对应 TS packages/core/src/environment.ts。
package environment

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/hxgh776/beaver/internal/autostart"
)

// RuntimeTool 单个运行时探测结果。
type RuntimeTool struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Found   bool   `json:"found"`
	Version string `json:"version,omitempty"`
}

type spec struct {
	name string
	role string
}

var specs = []spec{
	{"node", "stdio server 与反向桥的运行时"},
	{"npx", "npx 型 server 启动"},
	{"uvx", "python 型 server 启动"},
	{"docker", "容器型 server 启动"},
	{"git", "通用版本控制"},
}

// Detect 探测全部运行时（并发执行，与 TS Promise.all 等价）。
func Detect() []RuntimeTool {
	out := make([]RuntimeTool, len(specs))
	done := make(chan struct{}, len(specs))
	for i, s := range specs {
		go func(i int, s spec) {
			out[i] = detectTool(s)
			done <- struct{}{}
		}(i, s)
	}
	for range specs {
		<-done
	}
	return out
}

// detectTool Windows 上 npx 等是 .cmd shim，直接 exec 找不到——候选名 + cmd /c 兜底。
func detectTool(s spec) RuntimeTool {
	var candidates [][]string
	if runtime.GOOS == "windows" {
		candidates = [][]string{
			{s.name + ".exe", "--version"},
			{"cmd", "/c", s.name + ".cmd", "--version"},
			{"cmd", "/c", s.name, "--version"},
		}
	} else {
		candidates = [][]string{{s.name, "--version"}}
	}
	for _, argv := range candidates {
		if v := tryVersion(argv[0], argv[1:]); v != nil {
			t := RuntimeTool{Name: s.name, Role: s.role, Found: true}
			if *v != "" {
				t.Version = *v
			}
			return t
		}
	}
	return RuntimeTool{Name: s.name, Role: s.role, Found: false}
}

func tryVersion(command string, args []string) *string {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, command, args...)
	autostart.HideWindow(probe) // daemon 无控制台，探测命令不弹窗
	out, err := probe.Output()
	if err != nil {
		return nil
	}
	first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if len(first) > 60 {
		first = first[:60]
	}
	r := fmt.Sprint(first)
	return &r
}
