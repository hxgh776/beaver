// Package adapters 实现 10 个 AI coding agent 的配置适配器（读取/受管写入/归一化）。
// 对应 TS 侧 packages/core/src/adapters/*，行为 1:1 对齐。
package adapters

import (
	"github.com/hxgh776/beaver/internal/jsonv"
	"github.com/hxgh776/beaver/internal/store"
)

// RawServerEntry agent 配置文件里一个 server 条目归一化后的形态（读取方向）。
type RawServerEntry struct {
	Transport store.Transport
	Command   *string
	Args      []string
	Env       map[string]string
	Cwd       *string
	URL       *string
	Headers   map[string]string
}

// FragmentWrite 一次受管写入：Entry 为 nil 表示删除该键。
type FragmentWrite struct {
	Key   string
	Entry *RawServerEntry
}

type ConflictResolution string

const (
	ResolutionOverride ConflictResolution = "override"
	ResolutionSkip     ConflictResolution = "skip"
)

type ConflictInfo struct {
	Key          string
	ExpectedHash string
	CurrentHash  string
	Resolution   ConflictResolution
}

type ApplyChange struct {
	Key    string
	Action string // upsert | remove | none
}

// Journal S0 发布日志：rename 前记 pre/planned 意图，成功后清账；崩溃后前滚判定。
// *writeengine.PublicationJournal 天然满足该接口。
type Journal interface {
	Record(filePath string, preText *string, plannedText string) error
	Complete(filePath string) error
}

type ApplyOptions struct {
	// Previous 上次同步记录的管辖片段 hash（C8 冲突检测依据）
	Previous []store.KeyState
	// Resolutions 冲突处置，缺省 skip（宁可不动，不覆盖用户手改）
	Resolutions map[string]ConflictResolution
	DryRun      bool
	// Backup 首次实际变更前调用一次（同步引擎在此挂滚动备份）
	Backup func() error
	// Journal 发布日志
	Journal Journal
}

type ApplyResult struct {
	Changes   []ApplyChange
	KeyStates []store.KeyState
	Conflicts []ConflictInfo
}

// NamedEntry Read 的有序结果（文件中的出现顺序）。
type NamedEntry struct {
	Key   string
	Entry RawServerEntry
}

// Adapter 是单个 agent 的配置适配器契约。
type Adapter interface {
	Type() string
	DisplayName() string
	// Transports 该 agent 原生支持的传输——决定直连分发可写哪些 server
	Transports() []store.Transport
	// CandidatePaths 候选配置文件（按优先级；trae 存在 trae/trae-cn 两个变体）
	CandidatePaths() []string
	// Read 读取文件中全部 server 条目（归一化）；文件不存在返回空表
	Read(file string) ([]NamedEntry, error)
	// Apply 受管写入：只动本工具管辖的键，其余内容保真（C4/C6）
	Apply(file string, writes []FragmentWrite, options ApplyOptions) (ApplyResult, error)
	// NormalizeEntry agent 原生条目 → 归一化；无法识别返回 nil
	NormalizeEntry(raw *jsonv.Value) *RawServerEntry
	// DenormalizeEntry 归一化条目 → agent 原生形态（写入方向；不支持的传输应报错）
	DenormalizeEntry(entry *RawServerEntry) (*jsonv.Value, error)
}
