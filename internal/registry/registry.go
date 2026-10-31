// Package registry 注册表 CRUD、agent 探测与冷启动导入。
// 对应 TS 侧 packages/core/src/registry.ts（本期只移植 sync 测试所需面；
// 分组 CRUD 归属 M1）。
package registry

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hxgh776/beaver/internal/adapters"
	"github.com/hxgh776/beaver/internal/store"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func Slugify(name string) string {
	lower := strings.ToLower(name)
	var b strings.Builder
	prevDash := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 48 {
		slug = slug[:48]
		// TS 的 slice 可能以 '-' 结尾（replace 在 slice 前做），这里保持一致：
		// TS: replace(/^-+|-+$/g,'') 在 slice(0,48) 之前执行，尾部可能残留 '-'
	}
	if slug == "" {
		slug = "server"
	}
	return slug
}

func UniqueID(config *store.Config, base string) string {
	slug := base
	if !slugRe.MatchString(base) {
		slug = Slugify(base)
	}
	id := slug
	n := 2
	for {
		taken := false
		for _, s := range config.Servers {
			if s.ID == id {
				taken = true
				break
			}
		}
		if !taken {
			break
		}
		id = fmt.Sprintf("%s-%d", slug, n)
		n++
	}
	return id
}

// UpsertServerInput 可选字段用指针：nil = TS undefined（不覆盖/不写入）。
type UpsertServerInput struct {
	ID            string                         `json:"id,omitempty"`
	Name          string                         `json:"name"`
	Transport     store.Transport                `json:"transport"`
	Command       *string                        `json:"command,omitempty"`
	Args          *[]string                      `json:"args,omitempty"`
	Env           *map[string]string             `json:"env,omitempty"`
	Cwd           *string                        `json:"cwd,omitempty"`
	URL           *string                        `json:"url,omitempty"`
	Headers       *map[string]string             `json:"headers,omitempty"`
	GatewayMode   *bool                          `json:"gatewayMode,omitempty"`
	Enabled       *bool                          `json:"enabled,omitempty"`
	ToolOverrides *map[string]store.ToolOverride `json:"toolOverrides,omitempty"`
	Concurrency   *int                           `json:"concurrency,omitempty"`
}

// UpsertServer 直接改写传入的 config，调用方负责持久化。
func UpsertServer(config *store.Config, input UpsertServerInput) *store.ServerDef {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if input.ID != "" {
		for _, existing := range config.Servers {
			if existing.ID == input.ID {
				applyUpsert(existing, input)
				existing.UpdatedAt = now
				return existing
			}
		}
	}
	base := input.ID
	if base == "" {
		base = input.Name
	}
	id := UniqueID(config, base)
	def := &store.ServerDef{
		ID:        id,
		Name:      input.Name,
		Transport: input.Transport,
		// cleanUndefined(input) 的展开位
		Command:       input.Command,
		Args:          input.Args,
		Env:           input.Env,
		Cwd:           input.Cwd,
		URL:           input.URL,
		Headers:       input.Headers,
		GatewayMode:   input.GatewayMode != nil && *input.GatewayMode,
		Enabled:       input.Enabled == nil || *input.Enabled,
		ToolOverrides: input.ToolOverrides,
		Concurrency:   input.Concurrency,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	config.Servers = append(config.Servers, def)
	return def
}

func applyUpsert(existing *store.ServerDef, input UpsertServerInput) {
	existing.Name = input.Name
	existing.Transport = input.Transport
	if input.Command != nil {
		existing.Command = input.Command
	}
	if input.Args != nil {
		existing.Args = input.Args
	}
	if input.Env != nil {
		existing.Env = input.Env
	}
	if input.Cwd != nil {
		existing.Cwd = input.Cwd
	}
	if input.URL != nil {
		existing.URL = input.URL
	}
	if input.Headers != nil {
		existing.Headers = input.Headers
	}
	if input.GatewayMode != nil {
		existing.GatewayMode = *input.GatewayMode
	}
	if input.Enabled != nil {
		existing.Enabled = *input.Enabled
	}
	if input.ToolOverrides != nil {
		existing.ToolOverrides = input.ToolOverrides
	}
	if input.Concurrency != nil {
		existing.Concurrency = input.Concurrency
	}
}

func DeleteServer(config *store.Config, id string) {
	kept := config.Servers[:0]
	for _, s := range config.Servers {
		if s.ID != id {
			kept = append(kept, s)
		}
	}
	config.Servers = kept
	keptB := config.Bindings[:0]
	for _, b := range config.Bindings {
		if b.ServerID != id {
			keptB = append(keptB, b)
		}
	}
	config.Bindings = keptB
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for gi := range config.Groups {
		if slices.Contains(config.Groups[gi].ServerIDs, id) {
			ids := config.Groups[gi].ServerIDs[:0]
			for _, sid := range config.Groups[gi].ServerIDs {
				if sid != id {
					ids = append(ids, sid)
				}
			}
			config.Groups[gi].ServerIDs = ids
			config.Groups[gi].UpdatedAt = now
		}
	}
	// 对应的 agent 配置片段由下一次 sync 时按 syncState 收敛移除
}

func SetBinding(config *store.Config, serverID, agentType string) error {
	found := false
	for _, s := range config.Servers {
		if s.ID == serverID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("server 不存在: %s", serverID)
	}
	for _, b := range config.Bindings {
		if b.ServerID == serverID && b.AgentType == agentType {
			return nil
		}
	}
	config.Bindings = append(config.Bindings, store.AgentBinding{
		ServerID:  serverID,
		AgentType: agentType,
		AddedAt:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	return nil
}

func RemoveBinding(config *store.Config, serverID, agentType string) {
	kept := config.Bindings[:0]
	for _, b := range config.Bindings {
		if !(b.ServerID == serverID && b.AgentType == agentType) {
			kept = append(kept, b)
		}
	}
	config.Bindings = kept
}

// EffectiveBoundIDs agent 的有效绑定 server 集合（直连 ∪ 分组），供网关过滤与 UI 展示共用。
func EffectiveBoundIDs(config *store.Config, agentType string) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, b := range config.Bindings {
		if b.AgentType == agentType {
			ids[b.ServerID] = struct{}{}
		}
	}
	for _, g := range config.Groups {
		if slices.Contains(g.AgentTypes, agentType) {
			for _, sid := range g.ServerIDs {
				ids[sid] = struct{}{}
			}
		}
	}
	return ids
}

// BoundServers = servers 过滤出有效绑定集合。
func BoundServers(config *store.Config, agentType string) []*store.ServerDef {
	ids := EffectiveBoundIDs(config, agentType)
	out := []*store.ServerDef{}
	for _, s := range config.Servers {
		if _, ok := ids[s.ID]; ok {
			out = append(out, s)
		}
	}
	return out
}

// —— Agent 探测（C1） —— //

type AgentRef struct {
	DisplayName string
	Adapter     adapters.Adapter
}

type AgentDetection struct {
	AgentType   string
	DisplayName string
	// Detected 候选配置文件任一存在即视为已安装（配置可能是卸载残留，但足以判定可用）
	Detected bool
	// ConfigPaths 存在的配置文件路径；都不存在时返回全部候选路径（供 UI 展示与分发写入选址）
	ConfigPaths []string
	Transports  []store.Transport
}

// DetectAgents 判定标准只有一条：任一候选配置文件存在 → 已检测到。
func DetectAgents(agents map[string]AgentRef) []AgentDetection {
	out := []AgentDetection{}
	for _, def := range adapters.AgentDefinitions("") {
		src, ok := agents[def.Type]
		if !ok {
			continue
		}
		// 路径取自传入的 agents 表（支持沙箱重定向），展示名/传输取自同一来源
		paths := src.Adapter.CandidatePaths()
		existing := []string{}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				existing = append(existing, p)
			}
		}
		out = append(out, AgentDetection{
			AgentType:   def.Type,
			DisplayName: src.DisplayName,
			Detected:    len(existing) > 0,
			ConfigPaths: existingOrAll(existing, paths),
			Transports:  src.Adapter.Transports(),
		})
	}
	return out
}

func existingOrAll(existing, all []string) []string {
	if len(existing) > 0 {
		return existing
	}
	return all
}

// —— 导入（C5 冷启动） —— //

type ImportCandidate struct {
	AgentType   string                  `json:"agentType"`
	Key         string                  `json:"key"`
	Entry       adapters.RawServerEntry `json:"entry"`
	Fingerprint string                  `json:"fingerprint"`
}

type ImportCandidateGroup struct {
	Fingerprint string            `json:"fingerprint"`
	Candidates  []ImportCandidate `json:"candidates"`
	Suggested   UpsertServerInput `json:"suggested"`
}

func FingerprintOf(entry adapters.RawServerEntry) string {
	if entry.Transport == store.TransportStdio {
		return fmt.Sprintf("stdio|%s|%s", deref(entry.Command), strings.Join(entry.Args, " "))
	}
	return fmt.Sprintf("%s|%s", entry.Transport, deref(entry.URL))
}

// ImportPreview 读取各 agent 现有配置，产出导入候选（跨 agent 按指纹去重分组）。
func ImportPreview(agents map[string]AgentRef) ([]ImportCandidateGroup, error) {
	all := []ImportCandidate{}
	for _, def := range adapters.AgentDefinitions("") {
		src, ok := agents[def.Type]
		if !ok {
			continue
		}
		for _, file := range src.Adapter.CandidatePaths() {
			servers, err := src.Adapter.Read(file)
			if err != nil {
				return nil, err
			}
			for _, named := range servers {
				// 自身写入的网关公共条目（含更名前旧键）不是导入候选（Issue-1）
				if named.Key == store.GatewayKey || named.Key == store.LegacyGatewayKey {
					continue
				}
				all = append(all, ImportCandidate{
					AgentType:   def.Type,
					Key:         named.Key,
					Entry:       named.Entry,
					Fingerprint: FingerprintOf(named.Entry),
				})
			}
		}
	}
	groups := []ImportCandidateGroup{}
	index := map[string]int{}
	for _, c := range all {
		if gi, ok := index[c.Fingerprint]; ok {
			groups[gi].Candidates = append(groups[gi].Candidates, c)
			continue
		}
		groups = append(groups, ImportCandidateGroup{
			Fingerprint: c.Fingerprint,
			Candidates:  []ImportCandidate{c},
			Suggested:   toUpsertInput(c.Key, c.Entry),
		})
		index[c.Fingerprint] = len(groups) - 1
	}
	return groups, nil
}

// ApplyImport 将选中的候选写入注册表（跳过与现有 server 指纹重复的项）。
func ApplyImport(config *store.Config, groups []ImportCandidateGroup, existingFingerprints map[string]struct{}) []*store.ServerDef {
	added := []*store.ServerDef{}
	seen := existingFingerprints
	if seen == nil {
		seen = map[string]struct{}{}
		for _, s := range config.Servers {
			seen[FingerprintOfServer(s)] = struct{}{}
		}
	}
	for _, g := range groups {
		if _, ok := seen[g.Fingerprint]; ok {
			continue
		}
		seen[g.Fingerprint] = struct{}{}
		added = append(added, UpsertServer(config, g.Suggested))
	}
	return added
}

func FingerprintOfServer(s *store.ServerDef) string {
	args := []string{}
	if s.Args != nil {
		args = *s.Args
	}
	return fmt.Sprintf("stdio|%s|%s", deref(s.Command), strings.Join(args, " "))
}

func toUpsertInput(key string, entry adapters.RawServerEntry) UpsertServerInput {
	// TS 语义：entry.args/env/headers 为 undefined 时保持缺省（指针 nil = 不写入）
	in := UpsertServerInput{
		Name:      key,
		Transport: entry.Transport,
		Command:   entry.Command,
		Cwd:       entry.Cwd,
		URL:       entry.URL,
	}
	if entry.Args != nil {
		args := entry.Args
		in.Args = &args
	}
	if entry.Env != nil {
		env := entry.Env
		in.Env = &env
	}
	if entry.Headers != nil {
		headers := entry.Headers
		in.Headers = &headers
	}
	return in
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
