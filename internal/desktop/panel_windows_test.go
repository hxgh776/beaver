//go:build windows

package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 不可达端口：不复用 6280（用户真实 daemon），取 1 号端口必然无人监听。
const unreachableBase = "http://127.0.0.1:1"

func TestProbePanelBaseUnreachable(t *testing.T) {
	spec := defaultPanelSpec(unreachableBase, t.TempDir())
	if err := probePanelBase(spec); err == nil {
		t.Fatal("无监听地址应探测失败")
	}
}

func TestWritePanelErrorPage(t *testing.T) {
	home := t.TempDir()
	spec := defaultPanelSpec(unreachableBase, home)
	page, err := writePanelErrorPage(spec, os.ErrNotExist)
	if err != nil {
		t.Fatalf("错误页写入失败: %v", err)
	}
	if !strings.HasPrefix(page, "file://") {
		t.Fatalf("错误页应为 file:// 地址，得到 %q", page)
	}
	path := filepath.Join(home, "webview2", "panel-error.html")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("错误页文件不存在: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, unreachableBase) {
		t.Fatal("错误页应展示目标地址")
	}
	if !strings.Contains(body, "panel.log") {
		t.Fatal("错误页应展示诊断日志路径")
	}
}

// 核心回归：daemon 不可达时导航目标不再是那个空端口，而是可见的错误页，
// 同时 <home>/logs/panel.log 必须留下记录（静默白屏不再发生）。
func TestResolvePanelTargetWritesLogOnUnreachable(t *testing.T) {
	home := t.TempDir()
	spec := defaultPanelSpec(unreachableBase, home)
	target := resolvePanelTarget(spec)
	if target == unreachableBase {
		t.Fatal("daemon 不可达时不应再把空端口作为导航目标")
	}
	if !strings.HasPrefix(target, "file://") {
		t.Fatalf("应导航到本地错误页，得到 %q", target)
	}
	logPath := filepath.Join(home, "logs", "panel.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("panel.log 未写入: %v", err)
	}
	if !strings.Contains(string(data), "daemon 不可达") {
		t.Fatalf("panel.log 应记录不可达原因，内容: %s", data)
	}
}