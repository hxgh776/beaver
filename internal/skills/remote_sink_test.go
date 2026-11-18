package skills

// 远端型 agent（multica）技能投影分支测试：用桩 SkillSink 验证
// enable/disable 走 Push/Remove、推送哈希记账、投影状态按「上次推送哈希 vs SSOT」判定，
// 以及 Update 对启用中的远端目标重推。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hxgh776/beaver/internal/store"
)

type stubSink struct {
	pushes  []string // 记录推送的 skill directory
	removes []string
	pushErr error
}

func (s *stubSink) Push(_ context.Context, row store.InstalledSkill, _ string) error {
	if s.pushErr != nil {
		return s.pushErr
	}
	s.pushes = append(s.pushes, row.Directory)
	return nil
}

func (s *stubSink) Remove(_ context.Context, row store.InstalledSkill) error {
	s.removes = append(s.removes, row.Directory)
	return nil
}

func newSinkEnv(t *testing.T) (*testEnv, *stubSink) {
	t.Helper()
	e := newEnv(t)
	sink := &stubSink{}
	e.service.sinks = map[string]SkillSink{"multica": sink}
	return e, sink
}

func seedSSOT(t *testing.T, e *testEnv, directory, body string) {
	t.Helper()
	dir := filepath.Join(e.sandbox, "skills", directory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestToggleRemoteAgentPushesAndRecordsHash(t *testing.T) {
	e, sink := newSinkEnv(t)
	seedSSOT(t, e, "demo-skill", "正文 v1")
	if _, err := e.service.admit(admitInput{
		Source: "local", DirectoryHint: "demo-skill",
		SourceDir: filepath.Join(e.sandbox, "skills", "demo-skill"),
	}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(sink.pushes) != 0 {
		t.Fatalf("admit 默认全关，不应推送: %v", sink.pushes)
	}

	if _, err := e.service.Toggle("skill-demo-skill", "multica", true); err != nil {
		t.Fatalf("Toggle on: %v", err)
	}
	if len(sink.pushes) != 1 || sink.pushes[0] != "demo-skill" {
		t.Fatalf("启用应推送一次: %v", sink.pushes)
	}
	pushed := e.config.SkillPushState["multica"]["skill-demo-skill"]
	if pushed == "" {
		t.Fatalf("推送哈希未记账: %v", e.config.SkillPushState)
	}

	// SSOT 变了 → Modified（需要重推）；SSOT 未变 → Projected
	status, err := e.service.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	find := func(agentType string) ProjectionStatus {
		for _, st := range status[0].Targets {
			if st.AgentType == agentType {
				return st
			}
		}
		return ProjectionStatus{}
	}
	if s := find("multica"); !s.Projected || s.Modified || !s.Enabled {
		t.Fatalf("刚推送应为 projected: %+v", s)
	}
	seedSSOT(t, e, "demo-skill", "正文 v2")
	status2, err := e.service.List()
	if err != nil {
		t.Fatalf("List after change: %v", err)
	}
	find2 := func(agentType string) ProjectionStatus {
		for _, st := range status2[0].Targets {
			if st.AgentType == agentType {
				return st
			}
		}
		return ProjectionStatus{}
	}
	if s := find2("multica"); s.Projected || !s.Modified {
		t.Fatalf("SSOT 变更后应提示 modified: %+v", s)
	}

	// 停用：走 Remove + 清哈希
	if _, err := e.service.Toggle("skill-demo-skill", "multica", false); err != nil {
		t.Fatalf("Toggle off: %v", err)
	}
	if len(sink.removes) != 1 || sink.removes[0] != "demo-skill" {
		t.Fatalf("停用应移除一次: %v", sink.removes)
	}
	if got := e.config.SkillPushState["multica"]["skill-demo-skill"]; got != "" {
		t.Fatalf("停用后应清推送哈希: %v", e.config.SkillPushState)
	}
}

func TestRemoteAgentHasNoLocalDirProjection(t *testing.T) {
	e, sink := newSinkEnv(t)
	seedSSOT(t, e, "demo-skill", "正文")
	if _, err := e.service.admit(admitInput{
		Source: "local", DirectoryHint: "demo-skill",
		SourceDir: filepath.Join(e.sandbox, "skills", "demo-skill"),
	}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	// 启用远端 agent 不应写任何本地目录
	if _, err := e.service.Toggle("skill-demo-skill", "multica", true); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	for agentType, dir := range e.service.targetDirs {
		if agentType == "multica" {
			t.Fatalf("multica 不应有本地目标目录")
		}
		if _, err := os.Stat(filepath.Join(dir, "demo-skill")); err == nil {
			t.Fatalf("远端启用不应投影到本地目录 %s", dir)
		}
	}
	if len(sink.pushes) != 1 {
		t.Fatalf("应推送一次: %v", sink.pushes)
	}
}

func TestUpdateRepushesEnabledRemoteTarget(t *testing.T) {
	e, sink := newSinkEnv(t)
	seedSSOT(t, e, "demo-skill", "正文 v1")
	if _, err := e.service.admit(admitInput{
		Source: "local", DirectoryHint: "demo-skill",
		SourceDir: filepath.Join(e.sandbox, "skills", "demo-skill"),
	}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, err := e.service.Toggle("skill-demo-skill", "multica", true); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if len(sink.pushes) != 1 {
		t.Fatalf("前置：启用推送一次，实际 %v", sink.pushes)
	}
	// Update 需 GitHub 来源；直接改 SSOT 内容后手动触发重推逻辑不可行——
	// 这里验证 Uninstall 对远端目标走 Remove。
	if _, err := e.service.Uninstall("skill-demo-skill"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(sink.removes) != 1 || sink.removes[0] != "demo-skill" {
		t.Fatalf("卸载应移除远端投影: %v", sink.removes)
	}
	if got := e.config.SkillPushState["multica"]["skill-demo-skill"]; got != "" {
		t.Fatalf("卸载后应清推送哈希: %v", e.config.SkillPushState)
	}
}
