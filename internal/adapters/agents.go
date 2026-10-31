// 10 个 AI coding agent 的适配器组装。对应 TS 侧 packages/core/src/agents.ts。
package adapters

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/hxgh776/beaver/internal/store"
)

// AgentDefinition 单个 agent 的类型 + 展示名 + 适配器。
type AgentDefinition struct {
	Type        string
	DisplayName string
	Adapter     Adapter
}

// MakeAgents 构建全部 agent 定义。baseDir 传空时用用户主目录；
// 测试传入沙箱目录即可获得与生产完全一致的适配器组合。
func MakeAgents(baseDir string) map[string]*AgentDefinition {
	p := func(segments []string) string {
		if baseDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				home = "."
			}
			baseDir = home
		}
		return filepath.Join(append([]string{baseDir}, segments...)...)
	}

	agents := map[string]*AgentDefinition{}

	agents[store.AgentClaudeCode] = &AgentDefinition{
		Type:        store.AgentClaudeCode,
		DisplayName: "Claude Code",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentClaudeCode,
			DisplayName:  "Claude Code",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP, store.TransportSSE},
			Paths:        []string{p([]string{".claude.json"})},
			ContainerKey: "mcpServers",
			Normalize:    NormalizeClaudeStyle,
			Denormalize:  DenormalizeClaudeStyle,
		}),
	}

	agents[store.AgentCodex] = &AgentDefinition{
		Type:        store.AgentCodex,
		DisplayName: "Codex CLI",
		Adapter:     NewCodexTomlAdapter([]string{p([]string{".codex", "config.toml"})}),
	}

	agents[store.AgentCursor] = &AgentDefinition{
		Type:        store.AgentCursor,
		DisplayName: "Cursor",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentCursor,
			DisplayName:  "Cursor",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP},
			Paths:        []string{p([]string{".cursor", "mcp.json"})},
			ContainerKey: "mcpServers",
			Normalize:    NormalizeUrlOrCommand,
			Denormalize:  DenormalizeUrlOrCommand,
		}),
	}

	agents[store.AgentTrae] = &AgentDefinition{
		Type:        store.AgentTrae,
		DisplayName: "Trae",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentTrae,
			DisplayName:  "Trae",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP},
			Paths:        []string{p([]string{".trae", "mcp.json"}), p([]string{".trae-cn", "mcp.json"})},
			ContainerKey: "mcpServers",
			Normalize:    NormalizeUrlOrCommand,
			Denormalize:  DenormalizeUrlOrCommand,
		}),
	}

	agents[store.AgentOpencode] = &AgentDefinition{
		Type:        store.AgentOpencode,
		DisplayName: "OpenCode",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentOpencode,
			DisplayName:  "OpenCode",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP},
			Paths:        []string{p([]string{".config", "opencode", "opencode.json"})},
			ContainerKey: "mcp",
			Normalize:    NormalizeOpencode,
			Denormalize:  DenormalizeOpencode,
		}),
	}

	// M3.5：Claude Desktop（路径随平台不同）
	var claudeDesktopConfig string
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = p([]string{"AppData", "Roaming"})
		}
		claudeDesktopConfig = filepath.Join(appData, "Claude", "claude_desktop_config.json")
	case "darwin":
		claudeDesktopConfig = p([]string{"Library", "Application Support", "Claude", "claude_desktop_config.json"})
	default:
		claudeDesktopConfig = p([]string{".config", "Claude", "claude_desktop_config.json"})
	}
	agents[store.AgentClaudeDesktop] = &AgentDefinition{
		Type:        store.AgentClaudeDesktop,
		DisplayName: "Claude Desktop",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentClaudeDesktop,
			DisplayName:  "Claude Desktop",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP, store.TransportSSE},
			Paths:        []string{claudeDesktopConfig},
			ContainerKey: "mcpServers",
			Normalize:    NormalizeClaudeStyle,
			Denormalize:  DenormalizeClaudeStyle,
		}),
	}

	agents[store.AgentWindsurf] = &AgentDefinition{
		Type:        store.AgentWindsurf,
		DisplayName: "Windsurf",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentWindsurf,
			DisplayName:  "Windsurf",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP},
			Paths:        []string{p([]string{".codeium", "windsurf", "mcp_config.json"})},
			ContainerKey: "mcpServers",
			Normalize:    NormalizeUrlOrCommand,
			Denormalize:  DenormalizeUrlOrCommand,
		}),
	}

	agents[store.AgentGeminiCLI] = &AgentDefinition{
		Type:        store.AgentGeminiCLI,
		DisplayName: "Gemini CLI",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentGeminiCLI,
			DisplayName:  "Gemini CLI",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP},
			Paths:        []string{p([]string{".gemini", "settings.json"})},
			ContainerKey: "mcpServers",
			Normalize:    NormalizeGeminiStyle,
			Denormalize:  DenormalizeGeminiStyle,
		}),
	}

	// ZCode：用户级 MCP 在 ~/.zcode/cli/config.json 的 mcp.servers（嵌套容器键）。
	// schema 严格——未知键会被客户端丢弃，因此写入只用官方字段。
	agents[store.AgentZCode] = &AgentDefinition{
		Type:        store.AgentZCode,
		DisplayName: "ZCode",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentZCode,
			DisplayName:  "ZCode",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP, store.TransportSSE},
			Paths:        []string{p([]string{".zcode", "cli", "config.json"})},
			ContainerKey: "mcp.servers",
			Normalize:    NormalizeClaudeStyle,
			Denormalize:  DenormalizeClaudeStyle,
		}),
	}

	// WorkBuddy：~/.workbuddy/mcp.json，顶层 mcpServers；远程条目 type 为 streamableHttp
	agents[store.AgentWorkBuddy] = &AgentDefinition{
		Type:        store.AgentWorkBuddy,
		DisplayName: "WorkBuddy",
		Adapter: NewJsonAgentAdapter(JsonAdapterOptions{
			Type:         store.AgentWorkBuddy,
			DisplayName:  "WorkBuddy",
			Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP},
			Paths:        []string{p([]string{".workbuddy", "mcp.json"})},
			ContainerKey: "mcpServers",
			Normalize:    NormalizeWorkbuddy,
			Denormalize:  DenormalizeWorkbuddy,
		}),
	}

	return agents
}

// AgentDefinitions 按 AGENT_LIST 顺序返回全部定义。
func AgentDefinitions(baseDir string) []*AgentDefinition {
	m := MakeAgents(baseDir)
	return []*AgentDefinition{
		m[store.AgentClaudeCode], m[store.AgentCodex], m[store.AgentCursor], m[store.AgentTrae],
		m[store.AgentOpencode], m[store.AgentClaudeDesktop], m[store.AgentWindsurf],
		m[store.AgentGeminiCLI], m[store.AgentZCode], m[store.AgentWorkBuddy],
	}
}
