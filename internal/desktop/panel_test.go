package desktop

import (
	"path/filepath"
	"testing"
)

func TestDefaultPanelSpec(t *testing.T) {
	spec := defaultPanelSpec("http://127.0.0.1:6280", `C:\Users\demo\.beaver`)
	if spec.Title != PanelTitle {
		t.Fatalf("标题应为 %q，得到 %q", PanelTitle, spec.Title)
	}
	if spec.Width <= 0 || spec.Height <= 0 {
		t.Fatalf("初始尺寸应为正数，得到 %dx%d", spec.Width, spec.Height)
	}
	if spec.MinWidth <= 0 || spec.MinHeight <= 0 {
		t.Fatalf("最小尺寸应为正数，得到 %dx%d", spec.MinWidth, spec.MinHeight)
	}
	if got, want := spec.dataDir(), filepath.Join(spec.HomeDir, "webview2"); got != want {
		t.Fatalf("WebView2 用户数据目录应为 %q，得到 %q", want, got)
	}
	if got, want := spec.iconPath(), filepath.Join(spec.HomeDir, "webview2", "beaver.ico"); got != want {
		t.Fatalf("窗口图标缓存路径应为 %q，得到 %q", want, got)
	}
}

func TestNormalizeHome(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{`C:\Users\demo\.beaver`, `C:/Users/demo/.beaver`, true},
		{`c:\users\demo\.beaver\`, `C:\Users\demo\.beaver`, true},
		{`C:\Users\demo\.beaver`, `D:\Users\demo\.beaver`, false},
		{``, ``, true},
	}
	for _, c := range cases {
		if got := normalizeHome(c.a) == normalizeHome(c.b); got != c.same {
			t.Fatalf("normalizeHome(%q)=%q, normalizeHome(%q)=%q，期望相等 %v",
				c.a, normalizeHome(c.a), c.b, normalizeHome(c.b), c.same)
		}
	}
}
