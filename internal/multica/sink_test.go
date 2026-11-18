package multica

// SkillLibrarySink 测试：经真实 exec 通道（multicafake 重入）验证推送/移除的 CLI
// 编排——list → create/update → files 对齐；remove 按名查 id；正文剥离 frontmatter；
// 内容只走临时文件通道（--content-file）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxgh776/beaver/internal/multica/multicafake"
	"github.com/hxgh776/beaver/internal/store"
)

func sinkWithFakeCLI(t *testing.T) (*SkillLibrarySink, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "fakecli.log")
	t.Setenv(multicafake.Env, "1")
	t.Setenv(multicafake.EnvLog, logPath)
	return &SkillLibrarySink{CLI: func() *CLI {
		return &CLI{Binary: fakeCLICmd(t), WorkspaceID: "ws-1"}
	}}, logPath
}

func writeSkillDir(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeLog 一次 fake 调用一行：argv 行按 \x1f 分隔成 token 序列；content=/config= 行保持原文。
// 返回 (token 行列表, 原文)。
func fakeLog(t *testing.T, path string) ([][]string, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 fake 日志: %v", err)
	}
	text := string(data)
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" || strings.Contains(line, "config=") || strings.Contains(line, "content=") {
			continue
		}
		out = append(out, strings.Split(line, multicafake.ArgvSuffix))
	}
	return out, text
}

// callHas 某次调用的 token 序列包含全部 want。
func callHas(tokens []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, tok := range tokens {
			if tok == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func assertCall(t *testing.T, log [][]string, want ...string) {
	t.Helper()
	for _, tokens := range log {
		if callHas(tokens, want...) {
			return
		}
	}
	t.Fatalf("fake 日志缺少调用 %v", want)
}

func assertNoCall(t *testing.T, log [][]string, want ...string) {
	t.Helper()
	for _, tokens := range log {
		if callHas(tokens, want...) {
			t.Fatalf("fake 日志不应出现调用 %v", want)
		}
	}
}

func TestSkillLibrarySinkPushCreatesAndAlignsFiles(t *testing.T) {
	sink, logPath := sinkWithFakeCLI(t)
	dir := t.TempDir()
	writeSkillDir(t, dir, map[string]string{
		"SKILL.md":     "---\nname: demo\ndescription: 测试技能\n---\n\n正文内容",
		"scripts/a.sh": "echo hi",
	})
	row := store.InstalledSkill{ID: "skill-1", Directory: "demo-skill", Name: "demo"}

	if err := sink.Push(context.Background(), row, dir); err != nil {
		t.Fatalf("Push: %v", err)
	}
	log, text := fakeLog(t, logPath)
	assertCall(t, log, "skill", "list")
	assertCall(t, log, "skill", "create", "--name", "demo-skill", "--description", "测试技能")
	assertCall(t, log, "skill", "files", "list")
	assertCall(t, log, "skill", "files", "upsert", "--path", "scripts/a.sh")
	// SKILL.md 即内容本体：正文剥离 frontmatter 后经 --content-file 推送，不经 files upsert
	if !strings.Contains(text, "content=正文内容") {
		t.Fatalf("create 应携带剥离 frontmatter 的正文:\n%s", text)
	}
	if !strings.Contains(text, "content=echo hi") {
		t.Fatalf("files upsert 应携带附属文件内容:\n%s", text)
	}
	assertNoCall(t, log, "skill", "files", "upsert", "--path", "SKILL.md")
}

func TestSkillLibrarySinkPushUpdatesExisting(t *testing.T) {
	sink, logPath := sinkWithFakeCLI(t)
	dir := t.TempDir()
	writeSkillDir(t, dir, map[string]string{"SKILL.md": "无 frontmatter 正文"})
	// canned 库里有 browser-use（sk-1）→ 同名命中，走 update
	row := store.InstalledSkill{ID: "skill-1", Directory: "browser-use"}

	if err := sink.Push(context.Background(), row, dir); err != nil {
		t.Fatalf("Push: %v", err)
	}
	log, text := fakeLog(t, logPath)
	assertNoCall(t, log, "skill", "create")
	assertCall(t, log, "skill", "update", "sk-1")
	if !strings.Contains(text, "content=无 frontmatter 正文") {
		t.Fatalf("update 应携带全文:\n%s", text)
	}
}

func TestSkillLibrarySinkRemoveMissingIsIdempotent(t *testing.T) {
	sink, logPath := sinkWithFakeCLI(t)
	row := store.InstalledSkill{ID: "skill-1", Directory: "demo-skill"}

	// canned 库里只有 browser-use（sk-1）：同名不存在 → 幂等，无 delete
	if err := sink.Remove(context.Background(), row); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	log, _ := fakeLog(t, logPath)
	assertNoCall(t, log, "skill", "delete")
}

func TestSkillLibrarySinkRemoveHitsByName(t *testing.T) {
	sink, logPath := sinkWithFakeCLI(t)
	row := store.InstalledSkill{ID: "skill-1", Directory: "browser-use"}

	// canned 库里有 browser-use（sk-1）→ 按名命中并删除
	if err := sink.Remove(context.Background(), row); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	log, _ := fakeLog(t, logPath)
	assertCall(t, log, "skill", "delete", "sk-1")
}
