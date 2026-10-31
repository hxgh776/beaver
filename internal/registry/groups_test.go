package registry

// 分组 CRUD 测试（对齐 TS core registry.test 的分组语义）。

import (
	"testing"
	"time"

	"github.com/hxgh776/beaver/internal/store"
)

func baseConfigWithServers() *store.Config {
	cfg := store.DefaultConfig()
	UpsertServer(cfg, UpsertServerInput{ID: "alpha", Name: "Alpha", Transport: store.TransportStdio})
	UpsertServer(cfg, UpsertServerInput{ID: "beta", Name: "Beta", Transport: store.TransportStdio})
	return cfg
}

func strp2(s string) *string { return &s }

func TestUpsertGroupCreatesWithUniqueID(t *testing.T) {
	cfg := baseConfigWithServers()
	g := UpsertGroup(cfg, UpsertGroupInput{Name: "web", ServerIDs: []string{"alpha", "beta", "alpha", "ghost"}})
	if g.ID != "web" {
		t.Fatalf("id 应为 web，得到 %s", g.ID)
	}
	// 未知 server 被过滤，重复去重后保持顺序
	if len(g.ServerIDs) != 2 || g.ServerIDs[0] != "alpha" || g.ServerIDs[1] != "beta" {
		t.Fatalf("serverIds 不符: %v", g.ServerIDs)
	}
	// 同名再建 → -2 后缀
	g2 := UpsertGroup(cfg, UpsertGroupInput{Name: "web"})
	if g2.ID != "web-2" {
		t.Fatalf("重复名应为 web-2，得到 %s", g2.ID)
	}
}

func TestUpsertGroupNonASCIINamesGetGroupPrefix(t *testing.T) {
	cfg := baseConfigWithServers()
	g := UpsertGroup(cfg, UpsertGroupInput{Name: "网页服务"})
	if g.ID != "group" {
		t.Fatalf("纯非 ASCII 名 slug 为空 → group，得到 %s", g.ID)
	}
	g2 := UpsertGroup(cfg, UpsertGroupInput{Name: "网页服务二"})
	// "网页服务二" slug 为 "-2"?? —— TS slugify 后 trim '-' 为空 → group；第二个应为 group-2
	if g2.ID != "group-2" {
		t.Fatalf("第二个应为 group-2，得到 %s", g2.ID)
	}
}

func TestUpsertGroupUpdateByID(t *testing.T) {
	cfg := baseConfigWithServers()
	g := UpsertGroup(cfg, UpsertGroupInput{Name: "web", ServerIDs: []string{"alpha"}})
	createdAt := g.CreatedAt
	time.Sleep(2 * time.Millisecond) // 时间戳毫秒精度，避免同毫秒断言失效
	updated := UpsertGroup(cfg, UpsertGroupInput{ID: g.ID, Name: "web", ServerIDs: []string{"beta"}, AgentTypes: []string{store.AgentCursor}})
	if updated.ID != g.ID {
		t.Fatalf("更新不应换 id")
	}
	if len(updated.ServerIDs) != 1 || updated.ServerIDs[0] != "beta" {
		t.Fatalf("serverIds 应被整体替换: %v", updated.ServerIDs)
	}
	if updated.CreatedAt != createdAt {
		t.Fatalf("createdAt 不应变")
	}
	if updated.UpdatedAt == createdAt {
		t.Fatalf("updatedAt 应刷新")
	}
}

func TestGroupServerOperations(t *testing.T) {
	cfg := baseConfigWithServers()
	g := UpsertGroup(cfg, UpsertGroupInput{Name: "web"})

	if _, err := GroupAddServer(cfg, g.ID, "ghost"); err == nil {
		t.Fatalf("未知 server 应报错")
	}
	if _, err := GroupAddServer(cfg, "nope", "alpha"); err == nil {
		t.Fatalf("未知分组应报错")
	}
	added, _ := GroupAddServer(cfg, g.ID, "alpha")
	added, _ = GroupAddServer(cfg, g.ID, "alpha") // 幂等
	if len(added.ServerIDs) != 1 {
		t.Fatalf("重复添加应幂等: %v", added.ServerIDs)
	}

	removed, _ := GroupRemoveServer(cfg, g.ID, "alpha")
	if len(removed.ServerIDs) != 0 {
		t.Fatalf("移除后应为空: %v", removed.ServerIDs)
	}
}

func TestGroupAgentOperations(t *testing.T) {
	cfg := baseConfigWithServers()
	g := UpsertGroup(cfg, UpsertGroupInput{Name: "web"})
	bound, _ := GroupBindAgent(cfg, g.ID, store.AgentCursor)
	bound, _ = GroupBindAgent(cfg, g.ID, store.AgentCursor) // 幂等
	if len(bound.AgentTypes) != 1 {
		t.Fatalf("重复绑定应幂等: %v", bound.AgentTypes)
	}
	unbound, _ := GroupUnbindAgent(cfg, g.ID, store.AgentCursor)
	if len(unbound.AgentTypes) != 0 {
		t.Fatalf("解绑后应为空: %v", unbound.AgentTypes)
	}
}

func TestDeleteGroup(t *testing.T) {
	cfg := baseConfigWithServers()
	g := UpsertGroup(cfg, UpsertGroupInput{Name: "web"})
	DeleteGroup(cfg, g.ID)
	if len(cfg.Groups) != 0 {
		t.Fatalf("删除后应为空")
	}
}

func TestEffectiveBoundIDsUnion(t *testing.T) {
	cfg := baseConfigWithServers()
	UpsertServer(cfg, UpsertServerInput{ID: "gamma", Name: "Gamma", Transport: store.TransportStdio})
	SetBinding(cfg, "gamma", store.AgentCursor) // 直连
	g := UpsertGroup(cfg, UpsertGroupInput{Name: "web", ServerIDs: []string{"alpha"}, AgentTypes: []string{store.AgentCursor}})
	_ = g
	bound := EffectiveBoundIDs(cfg, store.AgentCursor)
	if len(bound) != 2 {
		t.Fatalf("直连 ∪ 分组应为 2 个，得到 %v", bound)
	}
	_, hasAlpha := bound["alpha"]
	_, hasGamma := bound["gamma"]
	if !hasAlpha || !hasGamma {
		t.Fatalf("应同时含 alpha 与 gamma: %v", bound)
	}
}
