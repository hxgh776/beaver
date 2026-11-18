// Multica：CLI 外置型 agent 适配器（VENU-15 重构）。
//
// multica 在 beaver 里是和 Claude Code / ZCode 并列的一个 agent——区别只在配置
// 落点：没有本地配置文件，beaver 把「绑定给 multica 的 server」收敛到 Multica
// workspace 的 MCP 库（workspace mcp add/update/remove）。至于 Multica 平台内部
// 再把这些条目分给它的哪个 agent，是 Multica 自己的事，beaver 不碰（VENU-15 用户口径）。
//
// 收敛语义（用户已确认「可删除的」）：库 = beaver 绑定集合。库中多出的条目——
// 无论历史遗留还是直接在 Multica 上建的——同步时移除；配置本体在 Multica 侧
// write-only 不可读，变更检测只依赖 beaver 自己的上次推送哈希（SyncState.KeyStates）。
package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hxgh776/beaver/internal/jsonv"
	"github.com/hxgh776/beaver/internal/multica"
	"github.com/hxgh776/beaver/internal/store"
)

// MulticaVirtualPath multica 的虚拟配置位置（报告/探测展示用；无真实文件）。
const MulticaVirtualPath = "multica://workspace"

// MulticaAdapter 把 multica workspace MCP 库当作 agent 配置的适配器。
// source 返回当前连接设置（daemon 注入；nil/缺省 = PATH 上的 multica CLI）。
type MulticaAdapter struct {
	source func() store.MulticaSettings
}

// NewMulticaAdapter 构造（source 为 nil 时用缺省 CLI 配置；daemon 启动后注入动态源）。
func NewMulticaAdapter() *MulticaAdapter { return &MulticaAdapter{} }

// SetSource 注入连接设置动态源（daemon.New 装配；设置变更即时生效）。
func (a *MulticaAdapter) SetSource(fn func() store.MulticaSettings) { a.source = fn }

func (a *MulticaAdapter) settings() store.MulticaSettings {
	if a.source != nil {
		return a.source()
	}
	return store.MulticaSettings{}
}

func (a *MulticaAdapter) cli() *multica.CLI {
	return multica.CLIFromSettings(a.settings())
}

func (a *MulticaAdapter) Type() string        { return store.AgentMultica }
func (a *MulticaAdapter) DisplayName() string { return "Multica" }

func (a *MulticaAdapter) Transports() []store.Transport {
	// 库条目就是 mcpServers 形状，stdio/http/sse 全部原生
	return []store.Transport{store.TransportStdio, store.TransportHTTP, store.TransportSSE}
}

func (a *MulticaAdapter) CandidatePaths() []string { return []string{MulticaVirtualPath} }

// Read 库条目配置本体 write-only 不可读 → 恒空（导入预览自然跳过 multica）。
func (a *MulticaAdapter) Read(file string) ([]NamedEntry, error) { return []NamedEntry{}, nil }

func (a *MulticaAdapter) NormalizeEntry(raw *jsonv.Value) *RawServerEntry {
	return NormalizeClaudeStyle(raw)
}

func (a *MulticaAdapter) DenormalizeEntry(entry *RawServerEntry) (*jsonv.Value, error) {
	return DenormalizeClaudeStyle(entry)
}

// IsRemote 远端型适配器标记：同步引擎据此跳过文件备份、隔离失败不拖垮其他 agent。
func (a *MulticaAdapter) IsRemote() bool { return true }

// DetectRemote 探测 = multica CLI 可解析（不发起登录/网络探测，保持 /api/agents 轻快）。
// location 是虚拟路径标识（无文件语义）。
func (a *MulticaAdapter) DetectRemote() (bool, string) {
	if _, err := a.cli().ResolveBinary(); err != nil {
		return false, MulticaVirtualPath
	}
	return true, MulticaVirtualPath
}

// mcpOp 一次库变更操作（Apply 内部规划产物）。
type mcpOp struct {
	key    string // 片段键（= server.ID，与文件型 agent 的配置键一致）
	id     string // 库条目 id（已存在时）
	config string // Entry != nil 时的推送 JSON
	hash   string // canonical 哈希（KeyStates 记账）
	action string // add | update | remove
}

// Apply 把片段写翻译成 workspace mcp 库操作。
//
//   - Entry != nil：库中已有同名条目 → 哈希未变则跳过（不动远端）、变了则 update；
//     不存在 → add；
//   - Entry == nil：已有同名条目 → remove（解绑/收敛移除）；
//   - 收敛：库中不在本次管辖集合里的条目一律 remove（严格收敛，用户确认可删除）；
//   - DryRun 不发起任何 CLI 调用，按绑定 + 上次状态推演计划。
func (a *MulticaAdapter) Apply(file string, writes []FragmentWrite, options ApplyOptions) (ApplyResult, error) {
	result := ApplyResult{
		Changes:   []ApplyChange{},
		KeyStates: []store.KeyState{},
		Conflicts: []ConflictInfo{},
	}
	prev := map[string]string{}
	for _, ks := range options.Previous {
		prev[ks.Key] = ks.Hash
	}

	// —— 规划（本地纯计算，DryRun 到此为止） —— //
	ops := []mcpOp{}
	managedKeys := map[string]bool{}
	for _, w := range writes {
		managedKeys[w.Key] = true
		if w.Entry == nil {
			// 删除意图：远端是否存在要等列表回来才知道
			ops = append(ops, mcpOp{key: w.Key, action: "remove"})
			continue
		}
		config, err := entryConfigJSON(w.Entry)
		if err != nil {
			return result, err
		}
		hash := entryHash(w.Entry)
		op := mcpOp{key: w.Key, config: config, hash: hash}
		switch {
		case prev[w.Key] == hash:
			// 上次推送内容一致 → 计划为 no-op；远端是否真在（被手动删了）按列表修正
			op.action = "none"
		default:
			op.action = "add" // 列表后若已存在改判 update
		}
		ops = append(ops, op)
	}

	if options.DryRun {
		for _, op := range ops {
			switch op.action {
			case "none":
				result.Changes = append(result.Changes, ApplyChange{Key: op.key, Action: "none"})
				result.KeyStates = append(result.KeyStates, store.KeyState{Key: op.key, Hash: op.hash})
			case "add":
				result.Changes = append(result.Changes, ApplyChange{Key: op.key, Action: "upsert"})
				result.KeyStates = append(result.KeyStates, store.KeyState{Key: op.key, Hash: op.hash})
			case "remove":
				if _, was := prev[op.key]; was {
					result.Changes = append(result.Changes, ApplyChange{Key: op.key, Action: "remove"})
				}
			}
		}
		return result, nil
	}

	// —— 实际执行 —— //
	cli := a.cli()
	ctx := context.Background()
	libs, err := cli.ListMCPServers(ctx)
	if err != nil {
		return result, err
	}
	existing := map[string]string{} // name → id
	for _, s := range libs {
		existing[s.Name] = s.ID
	}

	// add → 已存在改判 update（哈希一致则降级 no-op）；remove → 不存在降级 no-op
	final := make([]mcpOp, 0, len(ops))
	for _, op := range ops {
		id, found := existing[op.key]
		switch op.action {
		case "none":
			if !found {
				op.action = "add" // 上次推过但远端没了（被手动删）→ 重建
			}
		case "add":
			if found {
				op.action = "update"
				op.id = id
			}
		case "remove":
			if !found {
				continue // 远端本就没有 → no-op
			}
			op.id = id
		}
		final = append(final, op)
	}

	// 严格收敛：库中多出的条目（不归 beaver 管）→ 移除（「可删除的」，用户已确认）
	for name, id := range existing {
		if !managedKeys[name] {
			final = append(final, mcpOp{key: name, id: id, action: "remove"})
		}
	}

	// 顺序执行（每个 op 一次 CLI 子进程；远端库变更保持确定顺序，失败即停——
	// 已执行的部分幂等，下次同步重放即可收敛）
	for _, op := range final {
		if err := a.execOp(ctx, cli, op); err != nil {
			return result, fmt.Errorf("multica 库同步 %s 失败: %w", op.key, err)
		}
		switch op.action {
		case "remove":
			result.Changes = append(result.Changes, ApplyChange{Key: op.key, Action: "remove"})
		case "add", "update":
			result.Changes = append(result.Changes, ApplyChange{Key: op.key, Action: "upsert"})
			result.KeyStates = append(result.KeyStates, store.KeyState{Key: op.key, Hash: op.hash})
		case "none":
			result.Changes = append(result.Changes, ApplyChange{Key: op.key, Action: "none"})
			result.KeyStates = append(result.KeyStates, store.KeyState{Key: op.key, Hash: op.hash})
		}
	}
	return result, nil
}

func (a *MulticaAdapter) execOp(ctx context.Context, cli *multica.CLI, op mcpOp) error {
	switch op.action {
	case "add":
		return cli.AddMCPServer(ctx, op.key, op.config)
	case "update":
		return cli.UpdateMCPServer(ctx, op.id, "", op.config)
	case "remove":
		return cli.RemoveMCPServer(ctx, op.id)
	}
	return nil
}

// entryConfigJSON 归一化条目 → 推给 workspace mcp add/update 的 JSON（mcpServers 条目形状）。
func entryConfigJSON(entry *RawServerEntry) (string, error) {
	v, err := DenormalizeClaudeStyle(entry)
	if err != nil {
		return "", err
	}
	return jsonv.StringifyCompact(v), nil
}

// canonicalEntry 定序 JSON 形状（map 由 encoding/json 排序，哈希跨次运行稳定）。
type canonicalEntry struct {
	Transport store.Transport   `json:"transport"`
	Command   *string           `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Cwd       *string           `json:"cwd,omitempty"`
	URL       *string           `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// entryHash 条目内容指纹（变更检测：与 SyncState.KeyStates 上次推送哈希比对）。
func entryHash(entry *RawServerEntry) string {
	data, err := json.Marshal(canonicalEntry{
		Transport: entry.Transport,
		Command:   entry.Command,
		Args:      entry.Args,
		Env:       entry.Env,
		Cwd:       entry.Cwd,
		URL:       entry.URL,
		Headers:   entry.Headers,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
