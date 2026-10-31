// Package store 承载数据模型、config.json 的加载/校验/持久化与数据目录迁移链。
// 对应 TS 侧 packages/core/src/store.ts + types.ts，行为 1:1 对齐。
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hxgh776/beaver/internal/fsutil"
)

// —— 领域类型（字段顺序 = JSON 输出顺序 = TS zod schema 顺序） —— //

type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"
)

const (
	AgentClaudeCode    = "claude-code"
	AgentCodex         = "codex"
	AgentCursor        = "cursor"
	AgentTrae          = "trae"
	AgentOpencode      = "opencode"
	AgentClaudeDesktop = "claude-desktop"
	AgentWindsurf      = "windsurf"
	AgentGeminiCLI     = "gemini-cli"
	AgentZCode         = "zcode"
	AgentWorkBuddy     = "workbuddy"
	GatewayKey         = "beaver-gateway"
	LegacyGatewayKey   = "agent-control-gateway"
)

// AgentTypes 与 TS AGENT_TYPES 顺序一致（写入顺序 = 稳定性依赖）。
var AgentTypes = []string{
	AgentClaudeCode, AgentCodex, AgentCursor, AgentTrae, AgentOpencode,
	AgentClaudeDesktop, AgentWindsurf, AgentGeminiCLI, AgentZCode, AgentWorkBuddy,
}

func IsValidAgentType(t string) bool {
	for _, a := range AgentTypes {
		if a == t {
			return true
		}
	}
	return false
}

type ToolOverride struct {
	Enabled bool `json:"enabled"`
}

// ServerDef 注册表中的单一事实来源。可选字段用指针：nil = 缺省（TS undefined 语义），
// 空 map / 空数组非 nil 时照实输出（JSON.stringify 同样保留它们）。
type ServerDef struct {
	ID            string                   `json:"id"`
	Name          string                   `json:"name"`
	Transport     Transport                `json:"transport"`
	Command       *string                  `json:"command,omitempty"`
	Args          *[]string                `json:"args,omitempty"`
	Env           *map[string]string       `json:"env,omitempty"`
	Cwd           *string                  `json:"cwd,omitempty"`
	URL           *string                  `json:"url,omitempty"`
	Headers       *map[string]string       `json:"headers,omitempty"`
	GatewayMode   bool                     `json:"gatewayMode"`
	Enabled       bool                     `json:"enabled"`
	ToolOverrides *map[string]ToolOverride `json:"toolOverrides,omitempty"`
	Concurrency   *int                     `json:"concurrency,omitempty"`
	CreatedAt     string                   `json:"createdAt"`
	UpdatedAt     string                   `json:"updatedAt"`
}

type AgentBinding struct {
	ServerID  string `json:"serverId"`
	AgentType string `json:"agentType"`
	AddedAt   string `json:"addedAt"`
}

type ServerGroup struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	ServerIDs  []string `json:"serverIds"`
	AgentTypes []string `json:"agentTypes"`
	CreatedAt  string   `json:"createdAt"`
	UpdatedAt  string   `json:"updatedAt"`
}

type InstalledSkill struct {
	ID          string   `json:"id"`
	Directory   string   `json:"directory"`
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	Source      string   `json:"source"`
	RepoOwner   *string  `json:"repoOwner,omitempty"`
	RepoName    *string  `json:"repoName,omitempty"`
	Branch      *string  `json:"branch,omitempty"`
	SkillID     *string  `json:"skillId,omitempty"`
	Installs    *float64 `json:"installs,omitempty"`
	ReadmeURL   *string  `json:"readmeUrl,omitempty"`
	ContentHash string   `json:"contentHash"`
	InstalledAt string   `json:"installedAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

type SkillTarget struct {
	SkillID   string `json:"skillId"`
	AgentType string `json:"agentType"`
	Enabled   bool   `json:"enabled"`
}

type KeyState struct {
	Key  string `json:"key"`
	Hash string `json:"hash"`
}

type AgentSyncState struct {
	AgentType string     `json:"agentType"`
	KeyStates []KeyState `json:"keyStates"`
	UpdatedAt string     `json:"updatedAt"`
}

type Settings struct {
	Port            int     `json:"port"`
	Token           string  `json:"token"`
	LogLevel        string  `json:"logLevel"`
	TokenRotatedAt  *string `json:"tokenRotatedAt,omitempty"`
	AuthRequired    *bool   `json:"authRequired,omitempty"`
	RegistryBaseURL *string `json:"registryBaseUrl,omitempty"`
	// HasOnboarded 首启向导标记（M4）；缺失 = 未引导（存量用户以 servers/bindings 非空视作已引导）
	HasOnboarded *bool `json:"hasOnboarded,omitempty"`
}

type Config struct {
	Version      int                       `json:"version"`
	Servers      []*ServerDef              `json:"servers"`
	Bindings     []AgentBinding            `json:"bindings"`
	Groups       []ServerGroup             `json:"groups"`
	Skills       []InstalledSkill          `json:"skills"`
	SkillTargets []SkillTarget             `json:"skillTargets"`
	SyncState    map[string]AgentSyncState `json:"syncState"`
	Settings     Settings                  `json:"settings"`
}

func DefaultConfig() *Config {
	return &Config{
		Version:      1,
		Servers:      []*ServerDef{},
		Bindings:     []AgentBinding{},
		Groups:       []ServerGroup{},
		Skills:       []InstalledSkill{},
		SkillTargets: []SkillTarget{},
		SyncState:    map[string]AgentSyncState{},
		Settings: Settings{
			Port:         6280,
			Token:        randomToken(),
			LogLevel:     "info",
			AuthRequired: boolPtr(false), // D7：默认不启用令牌，仅环回监听兜底
		},
	}
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("随机源不可用: %v", err))
	}
	return hex.EncodeToString(b)
}

func boolPtr(b bool) *bool { return &b }

// —— 数据目录解析与迁移链 —— //

// ResolveHomeDir 优先级：explicit > BEAVER_HOME（兼容旧名 AGENT_CONTROL_HOME / MCP_MANAGER_HOME）
// > ~/.beaver（若存在旧目录 ~/.agent-control 或 ~/.mcp-manager 且新目录不存在，一次性整体迁移）。
func ResolveHomeDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	env := os.Getenv("BEAVER_HOME")
	if env == "" {
		env = os.Getenv("AGENT_CONTROL_HOME")
	}
	if env == "" {
		env = os.Getenv("MCP_MANAGER_HOME")
	}
	if env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	legacy := filepath.Join(home, ".agent-control")
	if !dirExists(legacy) {
		legacy = filepath.Join(home, ".mcp-manager")
		if !dirExists(legacy) {
			legacy = "" // 无旧目录
		}
	}
	if legacy == "" {
		legacy = filepath.Join(home, ".agent-control")
	}
	return MaybeMigrateHome(legacy, filepath.Join(home, ".beaver"))
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// MaybeMigrateHome 一次性整体迁移：旧目录存在且新目录不存在（或存在但为空）→ rename；
// 新目录已有内容或旧目录被占用（旧 daemon 在跑）→ 沿用现状，下次启动再迁。
func MaybeMigrateHome(legacyDir, newDir string) string {
	if !dirExists(legacyDir) && !fileExists(legacyDir) {
		return newDir
	}
	if dirExists(newDir) {
		entries, err := os.ReadDir(newDir)
		if err != nil {
			return newDir
		}
		if len(entries) > 0 {
			return newDir
		}
		if err := os.Remove(newDir); err != nil {
			return newDir
		}
	}
	if err := os.Rename(legacyDir, newDir); err != nil {
		return legacyDir // 下次启动再迁
	}
	return newDir
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// —— Store —— //

type Store struct {
	HomeDir string
}

func NewStore(homeDir string) *Store {
	return &Store{HomeDir: ResolveHomeDir(homeDir)}
}

func (s *Store) ConfigPath() string { return filepath.Join(s.HomeDir, "config.json") }
func (s *Store) BackupsDir() string { return filepath.Join(s.HomeDir, "backups") }
func (s *Store) LogsDir() string    { return filepath.Join(s.HomeDir, "logs") }
func (s *Store) PidPath() string    { return filepath.Join(s.HomeDir, "daemon.pid") }

// Load 加载配置：文件缺失 → 默认配置并落盘；文件损坏 → 移为 config.json.corrupt-<ts>
// 后重建默认配置（绝不静默丢弃用户数据之外的东西）。
func (s *Store) Load() (*Config, error) {
	text, ok, err := fsutil.ReadTextIfExists(s.ConfigPath())
	if err != nil {
		return nil, err
	}
	if !ok {
		config := DefaultConfig()
		if err := s.Save(config); err != nil {
			return nil, err
		}
		return config, nil
	}
	cfg, err := DecodeConfig([]byte(text))
	if err != nil {
		_ = os.Rename(s.ConfigPath(), fmt.Sprintf("%s.corrupt-%d", s.ConfigPath(), time.Now().UnixMilli()))
		config := DefaultConfig()
		if err := s.Save(config); err != nil {
			return nil, err
		}
		return config, nil
	}
	return cfg, nil
}

// Save 持久化配置；写前对旧 config.json 做滚动备份（keep=10）——
// M4「恢复备份」不只救 agent 配置，也救管理配置本身。
func (s *Store) Save(config *Config) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(config); err != nil {
		return err
	}
	data := buf.String()
	if prev, ok, _ := fsutil.ReadTextIfExists(s.ConfigPath()); ok {
		_ = s.rollConfigBackup(prev)
	}
	return fsutil.AtomicWriteFile(s.ConfigPath(), data)
}

// configBackupKeep config.json 滚动备份保留份数。
const configBackupKeep = 10

// ConfigBackupsDir config.json 备份目录（<home>/backups/config）。
func (s *Store) ConfigBackupsDir() string { return filepath.Join(s.BackupsDir(), "config") }

func (s *Store) rollConfigBackup(prev string) error {
	dir := s.ConfigBackupsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ts := strings.ReplaceAll(strings.ReplaceAll(time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), ":", "-"), ".", "-")
	if err := os.WriteFile(filepath.Join(dir, "config-"+ts+".json"), []byte(prev), 0o644); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if len(names) <= configBackupKeep {
			break
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// DecodeConfig 解析并校验 config.json（对应 TS zod configSchema.safeParse）。
// 未知字段静默剥离（zod 非严格对象同语义）；必填键缺失或值非法 → 报错（调用方走 corrupt 重建）。
func DecodeConfig(data []byte) (*Config, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	cfg := DefaultConfig()
	// 顶层必填键：version / servers / bindings / syncState / settings
	//（groups / skills / skillTargets 有 .default([])，缺省合法）
	for _, key := range []string{"version", "servers", "bindings", "syncState", "settings"} {
		if _, ok := raw[key]; !ok {
			return nil, fmt.Errorf("config.%s 缺失", key)
		}
	}
	if err := json.Unmarshal(raw["version"], &cfg.Version); err != nil {
		return nil, err
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("config.version 仅支持 1")
	}
	if err := json.Unmarshal(raw["servers"], &cfg.Servers); err != nil {
		return nil, fmt.Errorf("config.servers: %w", err)
	}
	for _, s := range cfg.Servers {
		if err := validateServer(s); err != nil {
			return nil, err
		}
	}
	if err := strictJSON(raw["bindings"], &cfg.Bindings); err != nil {
		return nil, fmt.Errorf("config.bindings: %w", err)
	}
	// .default([])：缺省迁移为空数组
	ensureSlice(&cfg.Groups)
	if err := strictJSON(rawOpt(raw, "groups"), &cfg.Groups); err != nil {
		return nil, fmt.Errorf("config.groups: %w", err)
	}
	ensureSlice(&cfg.Skills)
	if err := strictJSON(rawOpt(raw, "skills"), &cfg.Skills); err != nil {
		return nil, fmt.Errorf("config.skills: %w", err)
	}
	for i := range cfg.Skills {
		switch cfg.Skills[i].Source {
		case "github", "skills.sh", "local", "import":
		default:
			return nil, fmt.Errorf("config.skills[%d].source 非法: %s", i, cfg.Skills[i].Source)
		}
	}
	ensureSlice(&cfg.SkillTargets)
	if err := strictJSON(rawOpt(raw, "skillTargets"), &cfg.SkillTargets); err != nil {
		return nil, fmt.Errorf("config.skillTargets: %w", err)
	}
	if err := strictJSON(raw["syncState"], &cfg.SyncState); err != nil {
		return nil, fmt.Errorf("config.syncState: %w", err)
	}
	if cfg.SyncState == nil {
		return nil, fmt.Errorf("config.syncState 缺失")
	}
	if err := strictJSON(raw["settings"], &cfg.Settings); err != nil {
		return nil, fmt.Errorf("config.settings: %w", err)
	}
	s := &cfg.Settings
	if s.Port < 1 || s.Port > 65535 {
		return nil, fmt.Errorf("config.settings.port 越界: %d", s.Port)
	}
	if len(s.Token) < 8 {
		return nil, fmt.Errorf("config.settings.token 过短")
	}
	switch s.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return nil, fmt.Errorf("config.settings.logLevel 非法: %s", s.LogLevel)
	}
	return cfg, nil
}

func validateServer(s *ServerDef) error {
	if !slugRe.MatchString(s.ID) {
		return fmt.Errorf("config.servers.id 非法: %s", s.ID)
	}
	if s.Name == "" {
		return fmt.Errorf("config.servers.name 为空: %s", s.ID)
	}
	switch s.Transport {
	case TransportStdio, TransportHTTP, TransportSSE:
	default:
		return fmt.Errorf("config.servers.transport 非法: %s", s.Transport)
	}
	return nil
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// strictJSON 严格解码：未知字段报错？——不。zod 对象默认剥离未知字段，
// 这里同样允许未知字段（json.Unmarshal 天然忽略），保留 helper 以示语义。
func strictJSON(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

func rawOpt(raw map[string]json.RawMessage, key string) []byte {
	if v, ok := raw[key]; ok {
		return v
	}
	return []byte("[]")
}

func ensureSlice[T any](s *[]T) {
	if *s == nil {
		*s = []T{}
	}
}
