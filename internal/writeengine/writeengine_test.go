// 外部测试包：完整链路用例需要 syncengine（内部包会造成 import cycle）。
package writeengine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hxgh776/beaver/internal/registry"
	"github.com/hxgh776/beaver/internal/store"
	"github.com/hxgh776/beaver/internal/syncengine"
	"github.com/hxgh776/beaver/internal/writeengine"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeAgentFile(t *testing.T, sandbox string, rel []string, content string) string {
	t.Helper()
	file := filepath.Join(sandbox, filepath.Join(rel...))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// U-WE-01 首次写入前备份原始字节，且每文件只做一次
func TestFirstWriteBackupOnce(t *testing.T) {
	sandbox := t.TempDir()
	s := store.NewStore(sandbox)
	file := writeAgentFile(t, sandbox, []string{".cursor", "mcp.json"}, `{"mcpServers":{"a":{"command":"orig"}}}`)
	bak, err := writeengine.FirstWriteBackup(file, s.BackupsDir())
	if err != nil {
		t.Fatal(err)
	}
	if bak == "" {
		t.Fatal("首次备份应产生文件")
	}
	data, _ := os.ReadFile(bak)
	if string(data) != `{"mcpServers":{"a":{"command":"orig"}}}` {
		t.Fatalf("备份内容不符: %q", data)
	}
	entries, _ := os.ReadDir(filepath.Join(s.BackupsDir(), "first-write"))
	sources := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".source" {
			sources++
		}
	}
	if sources != 1 {
		t.Fatalf(".source 标记应为 1 个: %d", sources)
	}

	// 内容变化后再备份 → 返回空（已做过），备份保持首写时的字节
	if err := os.WriteFile(file, []byte(`{"changed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if again, err := writeengine.FirstWriteBackup(file, s.BackupsDir()); err != nil || again != "" {
		t.Fatalf("重复备份应返回空: %q %v", again, err)
	}
	files, _ := os.ReadDir(filepath.Join(s.BackupsDir(), "first-write"))
	var bakName string
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".bak" {
			bakName = f.Name()
		}
	}
	data, _ = os.ReadFile(filepath.Join(s.BackupsDir(), "first-write", bakName))
	if string(data) != `{"mcpServers":{"a":{"command":"orig"}}}` {
		t.Fatalf("备份应保持首写字节: %q", data)
	}
}

// U-WE-01 文件不存在时首写备份为 no-op；不同文件各自有独立备份
func TestFirstWriteBackupGhostAndDistinct(t *testing.T) {
	sandbox := t.TempDir()
	s := store.NewStore(sandbox)
	ghost := filepath.Join(sandbox, ".cursor", "nope.json")
	if bak, err := writeengine.FirstWriteBackup(ghost, s.BackupsDir()); err != nil || bak != "" {
		t.Fatalf("ghost 应 no-op: %q %v", bak, err)
	}
	a := writeAgentFile(t, sandbox, []string{"a.json"}, `{"x":1}`)
	b := writeAgentFile(t, sandbox, []string{"b.json"}, `{"y":2}`)
	if bak, _ := writeengine.FirstWriteBackup(a, s.BackupsDir()); bak == "" {
		t.Fatal("a 应有备份")
	}
	if bak, _ := writeengine.FirstWriteBackup(b, s.BackupsDir()); bak == "" {
		t.Fatal("b 应有备份")
	}
	files, _ := os.ReadDir(filepath.Join(s.BackupsDir(), "first-write"))
	baks := 0
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".bak" {
			baks++
		}
	}
	if baks != 2 {
		t.Fatalf("应有两个独立备份: %d", baks)
	}
}

// U-WE-01 经 SyncEngine 完整链路：写一次即产生首写备份，发布日志正常清账
func TestFirstWriteBackupViaSyncEngine(t *testing.T) {
	sandbox := t.TempDir()
	s := store.NewStore(sandbox)
	engineOpts := syncengine.Options{
		BackupsDir: s.BackupsDir(),
		HomeDir:    s.HomeDir,
		Agents:     syncengine.DefaultAgentRefs(sandbox),
	}
	config, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	cmd := "npx"
	args := []string{"-y", "srv-fs"}
	registry.UpsertServer(config, registry.UpsertServerInput{
		ID: "fs", Name: "fs", Transport: store.TransportStdio, Command: &cmd, Args: &args,
	})
	if err := registry.SetBinding(config, "fs", store.AgentCursor); err != nil {
		t.Fatal(err)
	}
	file := writeAgentFile(t, sandbox, []string{".cursor", "mcp.json"}, `{"mcpServers":{}}`)
	if _, err := syncengine.Sync(config, engineOpts, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}

	fwDir := filepath.Join(s.BackupsDir(), "first-write")
	entries, _ := os.ReadDir(fwDir)
	baks := []string{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".bak" {
			baks = append(baks, e.Name())
		}
	}
	if len(baks) != 1 {
		t.Fatalf("应有一个首写备份: %d", len(baks))
	}
	data, _ := os.ReadFile(filepath.Join(fwDir, baks[0]))
	if string(data) != `{"mcpServers":{}}` {
		t.Fatalf("首写备份内容不符: %q", data)
	}
	// 成功发布 → 发布日志不存在
	if _, err := os.Stat(filepath.Join(s.HomeDir, "pending-publication.json")); !os.IsNotExist(err) {
		t.Fatal("发布日志应已清账")
	}
	content, _ := os.ReadFile(file)
	if !regexp.MustCompile(`"fs"`).Match(content) {
		t.Fatalf("写入应包含 fs: %q", content)
	}
}

// U-WE-02 发布日志三态前滚
func TestRecoverPendingPublicationsThreeStates(t *testing.T) {
	sandbox := t.TempDir()
	pre := `{"mcpServers":{}}`
	planned := `{"mcpServers":{"a":{"command":"new"}}}`
	fileA := writeAgentFile(t, sandbox, []string{"a.json"}, pre)
	fileB := writeAgentFile(t, sandbox, []string{"b.json"}, pre)
	fileC := writeAgentFile(t, sandbox, []string{"c.json"}, pre)

	journal := writeengine.NewPublicationJournal(sandbox)
	if err := journal.Record(fileA, &pre, planned); err != nil { // A：rename 前崩溃 → 未发布
		t.Fatal(err)
	}
	if err := journal.Record(fileB, &pre, planned); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte(planned), 0o644); err != nil { // B：rename 已发生 → 已发布
		t.Fatal(err)
	}
	if err := journal.Record(fileC, &pre, planned); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileC, []byte(`{"user":"edited"}`), 0o644); err != nil { // C：发布后被用户改
		t.Fatal(err)
	}
	if err := journal.Complete(fileB); err != nil { // 模拟 B 清账后崩溃（日志仍在）
		t.Fatal(err)
	}

	report, err := writeengine.RecoverPendingPublications(sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if report == nil {
		t.Fatal("报告不应为 nil")
	}
	byPath := map[string]writeengine.PublicationRecoveryState{}
	for _, r := range report {
		byPath[r.Path] = r.State
	}
	if byPath[fileA] != writeengine.RecoveryUnpublished {
		t.Fatalf("A 应为 unpublished: %v", byPath[fileA])
	}
	if byPath[fileB] != writeengine.RecoveryPublished {
		t.Fatalf("B 应为 published: %v", byPath[fileB])
	}
	if byPath[fileC] != writeengine.RecoveryModifiedAfterPublish {
		t.Fatalf("C 应为 modified-after-publish: %v", byPath[fileC])
	}
	// 恢复只上报不回写；日志文件删除
	if _, err := os.Stat(filepath.Join(sandbox, "pending-publication.json")); !os.IsNotExist(err) {
		t.Fatal("日志应已删除")
	}
	data, _ := os.ReadFile(fileA)
	if string(data) != pre {
		t.Fatalf("A 内容不应被改: %q", data)
	}
}

// U-WE-02 无日志 → nil；损坏日志 → 挪到 .corrupt-* 留证
func TestRecoverPendingPublicationsCorrupt(t *testing.T) {
	sandbox := t.TempDir()
	if report, err := writeengine.RecoverPendingPublications(sandbox); err != nil || report != nil {
		t.Fatalf("无日志应返回 nil: %v %v", report, err)
	}
	journalFile := filepath.Join(sandbox, "pending-publication.json")
	if err := os.WriteFile(journalFile, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if report, err := writeengine.RecoverPendingPublications(sandbox); err != nil || report != nil {
		t.Fatalf("损坏日志应返回 nil: %v %v", report, err)
	}
	entries, _ := os.ReadDir(sandbox)
	found := false
	for _, e := range entries {
		if regexp.MustCompile(`^pending-publication\.json\.corrupt-\d+$`).MatchString(e.Name()) {
			found = true
		}
	}
	if !found {
		t.Fatal("未找到 .corrupt- 留证文件")
	}
}

// U-WE-02 record→complete 全清后删除日志；同文件重试覆盖条目
func TestJournalRecordCompleteAndRetry(t *testing.T) {
	sandbox := t.TempDir()
	journalFile := filepath.Join(sandbox, "pending-publication.json")
	journal := writeengine.NewPublicationJournal(sandbox)
	file := writeAgentFile(t, sandbox, []string{"x.json"}, "v1")
	v0, v1, v2 := "v0", "v1", "v2"
	if err := journal.Record(file, &v0, v1); err != nil {
		t.Fatal(err)
	}
	if err := journal.Record(file, &v1, v2); err != nil { // 重试覆盖
		t.Fatal(err)
	}
	data, _ := os.ReadFile(journalFile)
	var state struct {
		Version   int    `json:"version"`
		StartedAt string `json:"startedAt"`
		Files     []struct {
			Path        string  `json:"path"`
			PreHash     *string `json:"preHash"`
			PlannedHash string  `json:"plannedHash"`
			Done        bool    `json:"done"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Files) != 1 {
		t.Fatalf("重试应覆盖条目: %d", len(state.Files))
	}
	if state.Files[0].PlannedHash != sha(v2) {
		t.Fatalf("plannedHash 应为 sha(v2): %s", state.Files[0].PlannedHash)
	}
	if err := os.WriteFile(file, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := journal.Complete(file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journalFile); !os.IsNotExist(err) {
		t.Fatal("清账后日志应删除")
	}
}
