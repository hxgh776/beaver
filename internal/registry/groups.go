package registry

// 分组 CRUD（TS packages/core/src/registry.ts 的 upsertGroup/deleteGroup/group* 系列，行为 1:1）。
// 分发语义：分组包含 server，agent 关联分组；另支持直连绑定（bindings）。

import (
	"fmt"
	"time"

	"github.com/hxgh776/beaver/internal/store"
)

type UpsertGroupInput struct {
	ID         string
	Name       string
	ServerIDs  []string
	AgentTypes []string
}

func groupNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func UpsertGroup(config *store.Config, input UpsertGroupInput) *store.ServerGroup {
	now := groupNow()
	serverIDs := []string{}
	seen := map[string]struct{}{}
	for _, sid := range input.ServerIDs {
		if _, dup := seen[sid]; dup {
			continue
		}
		seen[sid] = struct{}{}
		for _, s := range config.Servers {
			if s.ID == sid {
				serverIDs = append(serverIDs, sid)
				break
			}
		}
	}
	agentTypes := []string{}
	typeSeen := map[string]struct{}{}
	for _, a := range input.AgentTypes {
		if _, dup := typeSeen[a]; dup {
			continue
		}
		typeSeen[a] = struct{}{}
		agentTypes = append(agentTypes, a)
	}
	if input.ID != "" {
		for i := range config.Groups {
			if config.Groups[i].ID == input.ID {
				config.Groups[i].Name = input.Name
				config.Groups[i].ServerIDs = serverIDs
				config.Groups[i].AgentTypes = agentTypes
				config.Groups[i].UpdatedAt = now
				return &config.Groups[i]
			}
		}
	}
	// 纯非 ASCII 名 slug 为空 → group 前缀（slugify 内部返回 "server"，这里换成 "group"）
	base := input.Name
	if !slugRe.MatchString(base) {
		base = Slugify(input.Name)
		if base == "server" {
			base = "group"
		}
	}
	id := base
	n := 2
	for {
		taken := false
		for _, g := range config.Groups {
			if g.ID == id {
				taken = true
				break
			}
		}
		if !taken {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
		n++
	}
	group := store.ServerGroup{
		ID:         id,
		Name:       input.Name,
		ServerIDs:  serverIDs,
		AgentTypes: agentTypes,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	config.Groups = append(config.Groups, group)
	return &config.Groups[len(config.Groups)-1]
}

func DeleteGroup(config *store.Config, id string) {
	out := config.Groups[:0]
	for _, g := range config.Groups {
		if g.ID != id {
			out = append(out, g)
		}
	}
	config.Groups = out
}

func requireGroup(config *store.Config, groupID string) (*store.ServerGroup, error) {
	for i := range config.Groups {
		if config.Groups[i].ID == groupID {
			return &config.Groups[i], nil
		}
	}
	return nil, fmt.Errorf("分组不存在: %s", groupID)
}

func GroupAddServer(config *store.Config, groupID, serverID string) (*store.ServerGroup, error) {
	found := false
	for _, s := range config.Servers {
		if s.ID == serverID {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("server 不存在: %s", serverID)
	}
	g, err := requireGroup(config, groupID)
	if err != nil {
		return nil, err
	}
	for _, sid := range g.ServerIDs {
		if sid == serverID {
			return g, nil
		}
	}
	g.ServerIDs = append(g.ServerIDs, serverID)
	g.UpdatedAt = groupNow()
	return g, nil
}

func GroupRemoveServer(config *store.Config, groupID, serverID string) (*store.ServerGroup, error) {
	g, err := requireGroup(config, groupID)
	if err != nil {
		return nil, err
	}
	out := g.ServerIDs[:0]
	for _, sid := range g.ServerIDs {
		if sid != serverID {
			out = append(out, sid)
		}
	}
	g.ServerIDs = out
	g.UpdatedAt = groupNow()
	return g, nil
}

func GroupBindAgent(config *store.Config, groupID, agentType string) (*store.ServerGroup, error) {
	g, err := requireGroup(config, groupID)
	if err != nil {
		return nil, err
	}
	for _, a := range g.AgentTypes {
		if a == agentType {
			return g, nil
		}
	}
	g.AgentTypes = append(g.AgentTypes, agentType)
	g.UpdatedAt = groupNow()
	return g, nil
}

func GroupUnbindAgent(config *store.Config, groupID, agentType string) (*store.ServerGroup, error) {
	g, err := requireGroup(config, groupID)
	if err != nil {
		return nil, err
	}
	out := g.AgentTypes[:0]
	for _, a := range g.AgentTypes {
		if a != agentType {
			out = append(out, a)
		}
	}
	g.AgentTypes = out
	g.UpdatedAt = groupNow()
	return g, nil
}
