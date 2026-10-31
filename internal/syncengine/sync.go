// Package syncengine 把注册表 + 绑定矩阵落成各 agent 的配置变更（C3/C8）。
// 对应 TS 侧 packages/core/src/sync.ts，行为 1:1 对齐。
//
//   - 直连 server → 按适配器写真实条目（键 = server.id）；
//   - gatewayMode server → http 能力 agent 写公共 GATEWAY_KEY 指向网关；
//   - 不支持 http 的 agent（如 Codex）→ G7 stdio 反向桥命令（桥 URL + token/headers）；
//   - 不再受管的片段（解绑/删除/直连转网关）按 syncState 收敛移除；
//   - 无桥可用且传输不支持 → 记入 unsupported 并跳过；
//   - S0：homeDir 提供时启用发布日志（写前意图）与首写字节备份。
package syncengine

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hxgh776/beaver/internal/adapters"
	"github.com/hxgh776/beaver/internal/backup"
	"github.com/hxgh776/beaver/internal/registry"
	"github.com/hxgh776/beaver/internal/secretbox"
	"github.com/hxgh776/beaver/internal/store"
	"github.com/hxgh776/beaver/internal/writeengine"
)

// StdioBridge G7 stdio 反向桥：command + 固定前缀参数，daemon 按自身安装路径注入。
type StdioBridge struct {
	Command  string
	BaseArgs []string
}

type Options struct {
	BackupsDir string
	// HomeDir S0：数据目录——提供后启用 pending-publication 发布日志
	HomeDir string
	Agents  map[string]registry.AgentRef
	// StdioBridge 反向桥配置；nil = 无桥可用
	StdioBridge *StdioBridge
}

type Input struct {
	DryRun      bool
	Resolutions map[string]adapters.ConflictResolution
}

// SyncChange TS 侧在报告层把 serverId 附加到 changes 上（网关公共键/收敛键为空）。
type SyncChange struct {
	Key      string  `json:"key"`
	Action   string  `json:"action"`
	ServerID *string `json:"serverId,omitempty"`
}

// SyncConflict 在适配器冲突信息上补充 resolutionKey（"<agentType>:<key>"）。
type SyncConflict struct {
	Key           string                      `json:"key"`
	ExpectedHash  string                      `json:"expectedHash"`
	CurrentHash   string                      `json:"currentHash"`
	Resolution    adapters.ConflictResolution `json:"resolution"`
	ResolutionKey string                      `json:"resolutionKey"`
}

type UnsupportedItem struct {
	ServerID string `json:"serverId"`
	Reason   string `json:"reason"`
}

type AgentSyncReport struct {
	AgentType   string            `json:"agentType"`
	File        string            `json:"file"`
	Changes     []SyncChange      `json:"changes"`
	Conflicts   []SyncConflict    `json:"conflicts"`
	Unsupported []UnsupportedItem `json:"unsupported"`
}

type Report struct {
	PerAgent []AgentSyncReport `json:"perAgent"`
}

func timeNowUTC() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// mapResolutions 把 API 层的冲突处置键（"<agentType>:<key>"）映射为适配器键（"<key>"）。
func mapResolutions(agentType string, resolutions map[string]adapters.ConflictResolution) map[string]adapters.ConflictResolution {
	if resolutions == nil {
		return nil
	}
	out := map[string]adapters.ConflictResolution{}
	prefix := agentType + ":"
	for k, v := range resolutions {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

// Sync 把注册表 + 绑定矩阵落成各 agent 的配置变更。
func Sync(config *store.Config, opts Options, input Input) (Report, error) {
	report := Report{PerAgent: []AgentSyncReport{}}
	agents := opts.Agents
	if agents == nil {
		agents = DefaultAgentRefs("")
	}
	// S0：一次 sync 全程共用一份发布日志；没有 homeDir（部分测试）则不启用
	var journal writeengine.Journal
	if opts.HomeDir != "" {
		journal = writeengine.NewPublicationJournal(opts.HomeDir)
	}
	gatewayEntryWritten := false
	for _, def := range adapters.AgentDefinitions("") {
		src, ok := agents[def.Type]
		if !ok {
			continue
		}
		agentReport, err := syncAgent(def.Type, src.Adapter, config, opts, input, journal)
		if err != nil {
			return Report{}, err
		}
		for _, c := range agentReport.Changes {
			if c.Key == store.GatewayKey && c.Action == "upsert" {
				gatewayEntryWritten = true
			}
		}
		report.PerAgent = append(report.PerAgent, agentReport)
	}
	// M2.4：网关条目已按新 token 重写 → 轮换提醒解除；
	// 或当前根本没有网关绑定（提醒已无对象）→ 一并解除
	anyGatewayBound := false
	for _, s := range config.Servers {
		if s.GatewayMode && s.Enabled {
			for _, b := range config.Bindings {
				if b.ServerID == s.ID {
					anyGatewayBound = true
					break
				}
			}
		}
	}
	if !input.DryRun && config.Settings.TokenRotatedAt != nil && (gatewayEntryWritten || !anyGatewayBound) {
		config.Settings.TokenRotatedAt = nil
	}
	return report, nil
}

func syncAgent(
	agentType string,
	adapter adapters.Adapter,
	config *store.Config,
	opts Options,
	input Input,
	journal writeengine.Journal,
) (AgentSyncReport, error) {
	dryRun := input.DryRun
	resolutions := mapResolutions(agentType, input.Resolutions)
	previous := []store.KeyState{}
	if st, ok := config.SyncState[agentType]; ok {
		previous = st.KeyStates
	}

	servers := registry.BoundServers(config, agentType)
	enabled := []*store.ServerDef{}
	for _, s := range servers {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	writes := []adapters.FragmentWrite{}
	unsupported := []UnsupportedItem{}
	// TS keyToServer：direct/bridge 键 → server.id；网关公共键 → undefined
	keyToServer := map[string]*string{}

	for _, server := range enabled {
		if server.GatewayMode {
			continue
		}
		native := containsTransport(adapter.Transports(), server.Transport)
		httpFamily := server.Transport == store.TransportHTTP || server.Transport == store.TransportSSE
		if native {
			// S4：静态加密的 env/headers 在直连分发时解密（agent 进程需真实读取）
			entry := toRawEntry(server)
			env, err := secretbox.DecryptRecord(entry.Env)
			if err != nil {
				return AgentSyncReport{}, err
			}
			entry.Env = env
			headers, err := secretbox.DecryptRecord(entry.Headers)
			if err != nil {
				return AgentSyncReport{}, err
			}
			entry.Headers = headers
			writes = append(writes, adapters.FragmentWrite{Key: server.ID, Entry: entry})
			id := server.ID
			keyToServer[server.ID] = &id
		} else if httpFamily && opts.StdioBridge != nil {
			// G7：经本地 stdio 桥使用远程 server
			writes = append(writes, adapters.FragmentWrite{
				Key:   server.ID,
				Entry: bridgeEntry(opts.StdioBridge, server.ID, deref(server.URL), derefMap(server.Headers), ""),
			})
			id := server.ID
			keyToServer[server.ID] = &id
		} else {
			reason := fmt.Sprintf("%s 不支持 %s 传输", agentType, server.Transport)
			if httpFamily {
				reason = fmt.Sprintf("%s 直连不支持 %s 传输且反向桥不可用", agentType, server.Transport)
			}
			unsupported = append(unsupported, UnsupportedItem{ServerID: server.ID, Reason: reason})
		}
	}

	hadGatewayKey := false
	for _, k := range previous {
		if k.Key == store.GatewayKey {
			hadGatewayKey = true
			break
		}
	}
	gatewayServers := []*store.ServerDef{}
	for _, s := range enabled {
		if s.GatewayMode {
			gatewayServers = append(gatewayServers, s)
		}
	}

	if len(gatewayServers) > 0 {
		if containsTransport(adapter.Transports(), store.TransportHTTP) {
			agentGatewayURL := fmt.Sprintf("http://127.0.0.1:%d/agents/%s/mcp", config.Settings.Port, agentType)
			withAuth := config.Settings.AuthRequired != nil && *config.Settings.AuthRequired
			gwEntry := &adapters.RawServerEntry{
				Transport: store.TransportHTTP,
				URL:       &agentGatewayURL,
			}
			if withAuth {
				token := config.Settings.Token
				gwEntry.Headers = map[string]string{"Authorization": "Bearer " + token}
			}
			writes = append(writes, adapters.FragmentWrite{Key: store.GatewayKey, Entry: gwEntry})
			// 项目更名前的旧公共键：顺手清理，避免在 agent 配置里残留死条目
			writes = append(writes, adapters.FragmentWrite{Key: store.LegacyGatewayKey})
		} else if opts.StdioBridge != nil {
			// G7：Codex 等经 stdio 桥接入分组网关端点
			agentGatewayURL := fmt.Sprintf("http://127.0.0.1:%d/agents/%s/mcp", config.Settings.Port, agentType)
			withAuth := config.Settings.AuthRequired != nil && *config.Settings.AuthRequired
			token := ""
			if withAuth {
				token = config.Settings.Token
			}
			writes = append(writes, adapters.FragmentWrite{
				Key:   store.GatewayKey,
				Entry: bridgeEntry(opts.StdioBridge, store.GatewayKey, agentGatewayURL, nil, token),
			})
			writes = append(writes, adapters.FragmentWrite{Key: store.LegacyGatewayKey}) // 旧键清理
		} else {
			ids := ""
			for i, s := range gatewayServers {
				if i > 0 {
					ids += ","
				}
				ids += s.ID
			}
			unsupported = append(unsupported, UnsupportedItem{
				ServerID: ids,
				Reason:   fmt.Sprintf("%s 原生不支持 http 传输且反向桥不可用", agentType),
			})
		}
	} else if hadGatewayKey {
		writes = append(writes,
			adapters.FragmentWrite{Key: store.GatewayKey},
			adapters.FragmentWrite{Key: store.LegacyGatewayKey}, // 旧键清理
		)
	}

	// 收敛：上次写过、本次不再受管的片段 → 移除
	managedKeys := map[string]struct{}{}
	for _, w := range writes {
		managedKeys[w.Key] = struct{}{}
	}
	for _, ks := range previous {
		if _, managed := managedKeys[ks.Key]; !managed {
			writes = append(writes, adapters.FragmentWrite{Key: ks.Key})
		}
	}

	file := resolveTargetFile(adapter)
	var backupFn func() error
	if !dryRun && opts.BackupsDir != "" {
		backupFn = func() error {
			if _, err := backup.BackupFile(file, agentType, opts.BackupsDir, 10); err != nil {
				return err
			}
			// S0：首次写入前的字节级原始备份（每文件只做一次）
			if _, err := writeengine.FirstWriteBackup(file, opts.BackupsDir); err != nil {
				return err
			}
			return nil
		}
	}

	result, err := adapter.Apply(file, writes, adapters.ApplyOptions{
		Previous:    previous,
		Resolutions: resolutions,
		DryRun:      dryRun,
		Backup:      backupFn,
		Journal:     journal,
	})
	if err != nil {
		return AgentSyncReport{}, err
	}

	if !dryRun {
		config.SyncState[agentType] = store.AgentSyncState{
			AgentType: agentType,
			KeyStates: result.KeyStates,
			UpdatedAt: timeNowUTC(),
		}
	}

	changes := make([]SyncChange, 0, len(result.Changes))
	for _, c := range result.Changes {
		change := SyncChange{Key: c.Key, Action: c.Action}
		if sid, ok := keyToServer[c.Key]; ok {
			change.ServerID = sid
		}
		changes = append(changes, change)
	}
	conflicts := make([]SyncConflict, 0, len(result.Conflicts))
	for _, c := range result.Conflicts {
		conflicts = append(conflicts, SyncConflict{
			Key:           c.Key,
			ExpectedHash:  c.ExpectedHash,
			CurrentHash:   c.CurrentHash,
			Resolution:    c.Resolution,
			ResolutionKey: agentType + ":" + c.Key,
		})
	}

	return AgentSyncReport{
		AgentType:   agentType,
		File:        file,
		Changes:     changes,
		Conflicts:   conflicts,
		Unsupported: unsupported,
	}, nil
}

// resolveTargetFile 候选中第一个存在的文件；都不存在时返回首个候选（写入时新建）。
func resolveTargetFile(adapter adapters.Adapter) string {
	paths := adapter.CandidatePaths()
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if len(paths) > 0 {
		return paths[0]
	}
	return ""
}

func containsTransport(list []store.Transport, t store.Transport) bool {
	for _, x := range list {
		if x == t {
			return true
		}
	}
	return false
}

func derefMap(m *map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	return *m
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// toRawEntry ServerDef → agent 原生条目（claude-style 形状，经适配器归一化语义）。
func toRawEntry(s *store.ServerDef) *adapters.RawServerEntry {
	entry := &adapters.RawServerEntry{Transport: s.Transport}
	if s.Command != nil {
		entry.Command = s.Command
	}
	if s.Args != nil {
		entry.Args = *s.Args
	}
	entry.Env = derefMap(s.Env)
	if s.Cwd != nil {
		entry.Cwd = s.Cwd
	}
	if s.URL != nil {
		entry.URL = s.URL
	}
	entry.Headers = derefMap(s.Headers)
	return entry
}

// bridgeEntry G7 反向桥条目：command + baseArgs + url + token/headers + --name。
// Authorization 头已由 --token 承载，跳过避免重复。
func bridgeEntry(bridge *StdioBridge, key, url string, headers map[string]string, token string) *adapters.RawServerEntry {
	args := append([]string{}, bridge.BaseArgs...)
	args = append(args, url)
	if token != "" {
		args = append(args, "--token="+token)
	}
	if headers != nil {
		for k, v := range headers {
			if k == "Authorization" || strings.EqualFold(k, "Authorization") {
				continue // token 已带 Authorization
			}
			args = append(args, "--header", fmt.Sprintf("%s=%s", k, v))
		}
	}
	args = append(args, "--name", key)
	return &adapters.RawServerEntry{
		Transport: store.TransportStdio,
		Command:   &bridge.Command,
		Args:      args,
	}
}

// DefaultAgentRefs 构建 agent 引用表（daemon/测试共用）。
func DefaultAgentRefs(baseDir string) map[string]registry.AgentRef {
	refs := map[string]registry.AgentRef{}
	for _, def := range adapters.AgentDefinitions(baseDir) {
		refs[def.Type] = registry.AgentRef{DisplayName: def.DisplayName, Adapter: def.Adapter}
	}
	return refs
}
