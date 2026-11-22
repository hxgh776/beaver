package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAppPathOutput(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "msedge.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := "\r\nHKEY_LOCAL_MACHINE\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\App Paths\\msedge.exe\r\n" +
		"    (\u9ed8\u8ba4)    REG_SZ    " + exe + "\r\n\r\n"
	if got := parseAppPathOutput(out); got != exe {
		t.Fatalf("应解析出 %q，得到 %q", exe, got)
	}
}

func TestParseAppPathOutputSkipsMissingPath(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "msedge.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "gone.exe")
	out := "    (Ĭ\xc8\xcf)    REG_SZ    " + missing + "\r\n" +
		"    (默认)    REG_SZ    " + exe + "\r\n"
	if got := parseAppPathOutput(out); got != exe {
		t.Fatalf("应跳过不存在的路径并解析出 %q，得到 %q", exe, got)
	}
}

func TestParseAppPathOutputEmpty(t *testing.T) {
	if got := parseAppPathOutput("\r\nERROR: 找不到指定的注册表项或值。\r\n"); got != "" {
		t.Fatalf("无 REG_SZ 时应返回空串，得到 %q", got)
	}
}