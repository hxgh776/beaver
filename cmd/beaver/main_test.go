package main

// 入口参数解析层回归测试（VENU-16 复审缺陷）：
// 修复前 main() 只摘 --home，`beaver --home X --agent-home Y`（无子命令）会把
// --agent-home 当未知子命令打印 usage 并 exit 2，而 `beaver tui --agent-home Y`
// 却能进面板——两条入口路径行为不一致。
// 本组测试钉住契约：全局 flag（--home/--agent-home/--port）位置不限、= 形式等价、
// --port 0 以 &0 透传（绑定随机空闲端口，非"未设置"）、未知 flag 仍 usage + exit 2。

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// resetFlags 用例之间清空全局 flag 状态（包级变量不跨用例泄漏）。
func resetFlags() {
	homeOverride = ""
	agentHomeOverride = ""
	portOverride = nil
}

func intPtr(v int) *int { return &v }

// assertGlobals 校验全局 flag 落值；wantPort 为 nil 表示未设置（nil 指针本身也是契约：
// `--port 0` 必须是 &0 而不是 nil，否则语义退化为"用配置端口"）。
func assertGlobals(t *testing.T, home, agentHome string, wantPort *int) {
	t.Helper()
	if homeOverride != home {
		t.Errorf("homeOverride = %q, 期望 %q", homeOverride, home)
	}
	if agentHomeOverride != agentHome {
		t.Errorf("agentHomeOverride = %q, 期望 %q", agentHomeOverride, agentHome)
	}
	switch {
	case wantPort == nil && portOverride != nil:
		t.Errorf("portOverride = %d, 期望未设置（nil）", *portOverride)
	case wantPort != nil && portOverride == nil:
		t.Errorf("portOverride = nil, 期望 %d", *wantPort)
	case wantPort != nil && *portOverride != *wantPort:
		t.Errorf("portOverride = %d, 期望 %d", *portOverride, *wantPort)
	}
}

// TestGlobalFlagsEnterTUI 无子命令入口：全局 flag 全部摘除、剩余参数为空 = 进 TUI。
// 这是复审缺陷的直接回归：--agent-home / --port 打头或出现在 --home 之后，都不能再被当成子命令。
func TestGlobalFlagsEnterTUI(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		home string
		agnt string
		port *int
	}{
		{"无参数", []string{}, "", "", nil},
		{"home单独", []string{"--home", "H"}, "H", "", nil},
		{"home等号形式", []string{"--home=H"}, "H", "", nil},
		{"三flag组合", []string{"--home", "H", "--agent-home", "A", "--port", "0"}, "H", "A", intPtr(0)},
		{"agentHome打头", []string{"--agent-home", "A", "--home", "H"}, "H", "A", nil},
		{"等号形式混用", []string{"--port=7000", "--agent-home=A"}, "", "A", intPtr(7000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlags()
			got := extractGlobalFlags(tc.argv)
			if len(got) != 0 {
				t.Fatalf("剩余参数 = %v，应为空（run() 据此进入 TUI 默认入口）", got)
			}
			assertGlobals(t, tc.home, tc.agnt, tc.port)
		})
	}
}

// TestGlobalFlagsWithSubcommand 有子命令时：全局 flag 照常摘除，子命令与其自身 flag 原样保留。
func TestGlobalFlagsWithSubcommand(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		rest []string
		home string
		agnt string
		port *int
	}{
		{"纯子命令", []string{"status"}, []string{"status"}, "", "", nil},
		{"home+子命令自身flag", []string{"--home", "H", "sync", "--dry-run"}, []string{"sync", "--dry-run"}, "H", "", nil},
		{"port全局摘除供daemon用", []string{"--port", "7000", "daemon"}, []string{"daemon"}, "", "", intPtr(7000)},
		{"bridge自有flag不受影响", []string{"bridge", "http://x/mcp", "--token=t", "--name=n"}, []string{"bridge", "http://x/mcp", "--token=t", "--name=n"}, "", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlags()
			got := extractGlobalFlags(tc.argv)
			if len(got) != len(tc.rest) {
				t.Fatalf("剩余参数 = %v, 期望 %v", got, tc.rest)
			}
			for i := range tc.rest {
				if got[i] != tc.rest[i] {
					t.Fatalf("剩余参数 = %v, 期望 %v", got, tc.rest)
				}
			}
			assertGlobals(t, tc.home, tc.agnt, tc.port)
		})
	}
}

// TestUnknownFlagUsageExit2 未知 flag / 未知子命令仍走 usage + exit 2（含非法 --port 值不摘除）。
func TestUnknownFlagUsageExit2(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"未知flag", []string{"--bogus"}},
		{"port值非法", []string{"--port", "abc"}},
		{"port等号值非法", []string{"--port=abc"}},
		{"未知子命令", []string{"frobnicate"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlags()
			stderr := captureStderr(t, func() {
				if code := run(tc.argv); code != 2 {
					t.Errorf("run(%v) 退出码 = %d, 期望 2（usage）", tc.argv, code)
				}
			})
			if !strings.Contains(stderr, "用法") {
				t.Errorf("stderr 未打印 usage:\n%s", stderr)
			}
		})
	}
}

// TestEntryFlagsReachDesktop 端到端：经 run() 真实入口分派进桌面面板——复现复审报告的两条
// 缺陷命令行，无参数必须落到桌面入口而不是 usage（exit 0）。全局 flag 落值才算通过。
// desktopEntry 在测试内替换为记录调用，避免真起托盘/窗口；桌面窗口渲染在真机冒烟验证。
func TestEntryFlagsReachDesktop(t *testing.T) {
	home := t.TempDir()
	agentHome := t.TempDir()
	t.Setenv("AGENT_CONTROL_AGENT_HOME", agentHome)

	cases := []struct {
		name string
		argv []string
		agnt string
	}{
		{"home+agentHome无子命令", []string{"--home", home, "--agent-home", agentHome}, agentHome},
		{"home+port0无子命令", []string{"--home", home, "--port", "0"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlags()
			reached := false
			saved := desktopEntry
			desktopEntry = func() error { reached = true; return nil }
			defer func() { desktopEntry = saved }()

			if code := run(tc.argv); code != 0 {
				t.Fatalf("run(%v) 退出码 = %d, 期望 0（进入桌面面板）", tc.argv, code)
			}
			if !reached {
				t.Fatalf("run(%v) 未分派到桌面入口", tc.argv)
			}
			if homeOverride != home {
				t.Errorf("homeOverride = %q, 期望 %q", homeOverride, home)
			}
			if tc.agnt != "" && agentHomeOverride != tc.agnt {
				t.Errorf("agentHomeOverride = %q, 期望 %q", agentHomeOverride, tc.agnt)
			}
		})
	}
}

// captureStderr 临时接管 os.Stderr，捕获 fn 期间的输出（usage 打印不污染测试日志）。
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()
	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String()
}
