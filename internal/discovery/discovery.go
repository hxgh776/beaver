// Package discovery 九源 MCP registry 搜索的条目映射与排序过滤。
// 对应 TS packages/core/src/registry-discovery.ts，行为 1:1。
// 上游 JSON 结构宽松（字段命名不一致），统一以 map[string]any 解码后手工映射。
package discovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hxgh776/beaver/internal/registry"
)

// Suggestion 单条发现结果。Suggestion 为 nil = 仅发现、无可执行安装配置。
type Suggestion struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Suggestion  *registry.UpsertServerInput `json:"suggestion"`
	Source      string                      `json:"source,omitempty"`
	UseCount    *float64                    `json:"useCount,omitempty"`
}

func floatPtr(f float64) *float64 { return &f }

func num(v any) *float64 {
	if f, ok := v.(float64); ok {
		return floatPtr(f)
	}
	return nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

// shortName registry 全名形如 com.github/user/pkg 或 io.github.user/pkg → 取尾段作展示名/id 基础
func shortName(full string) string {
	parts := strings.Split(full, "/")
	base := parts[len(parts)-1]
	trimmed := strings.TrimPrefix(base, "mcp-")
	if trimmed == "" {
		return full
	}
	return trimmed
}

// RegistryEntryToServerInput 官方 MCP Registry 条目 → 本注册表输入。
// 1) 远程端点优先：streamable-http > sse；2) 包：npm → npx、pypi → uvx
// （GitHub MCP Registry 的 registry_name 为空、只给 runtime_hint，一并推断）。
func RegistryEntryToServerInput(entry map[string]any) Suggestion {
	name := shortName(str(entry["name"]))
	description := str(entry["description"])
	remotes := asSlice(entry["remotes"])
	pickRemote := func(typ string) map[string]any {
		for _, r := range remotes {
			if m := asMap(r); m != nil && str(m["type"]) == typ {
				return m
			}
		}
		return nil
	}
	remote := pickRemote("streamable-http")
	if remote == nil {
		remote = pickRemote("sse")
	}
	if remote == nil && len(remotes) > 0 {
		remote = asMap(remotes[0])
	}
	if remote != nil && str(remote["url"]) != "" {
		transport := "http"
		if str(remote["type"]) == "sse" {
			transport = "sse"
		}
		return Suggestion{Name: name, Description: description, Suggestion: &registry.UpsertServerInput{
			Name: name, Transport: storeTransport(transport), URL: strPtr(str(remote["url"])), GatewayMode: boolPtr(true),
		}}
	}
	packages := asSlice(entry["packages"])
	if len(packages) > 0 {
		if pkg := asMap(packages[0]); pkg != nil {
			pkgName := str(pkg["identifier"])
			if pkgName == "" {
				pkgName = str(pkg["name"])
			}
			if pkgName != "" {
				registryType := strings.ToLower(firstNonEmpty(
					str(pkg["registryType"]),
					str(pkg["registry_name"]),
					hintToType(str(pkg["runtime_hint"])),
					"npm",
				))
				switch registryType {
				case "npm":
					return Suggestion{Name: name, Description: description, Suggestion: &registry.UpsertServerInput{
						Name: name, Transport: storeTransport("stdio"), Command: strPtr("npx"),
						Args: strSlicePtr([]string{"-y", pkgName}), GatewayMode: boolPtr(true),
					}}
				case "pypi":
					return Suggestion{Name: name, Description: description, Suggestion: &registry.UpsertServerInput{
						Name: name, Transport: storeTransport("stdio"), Command: strPtr("uvx"),
						Args: strSlicePtr([]string{pkgName}), GatewayMode: boolPtr(true),
					}}
				}
			}
		}
	}
	return Suggestion{Name: name, Description: description}
}

func hintToType(hint string) string {
	if hint == "uvx" || hint == "uv" {
		return "pypi"
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// NpmSearchToSuggestions npm 搜索回退：-/v1/search 结果 → npx 型建议（网关模式）。
func NpmSearchToSuggestions(data map[string]any) []Suggestion {
	out := []Suggestion{}
	for _, raw := range asSlice(data["objects"]) {
		obj := asMap(raw)
		if obj == nil {
			continue
		}
		pkg := asMap(obj["package"])
		if pkg == nil {
			continue
		}
		pkgName := str(pkg["name"])
		if pkgName == "" {
			continue
		}
		descParts := []string{}
		if d := str(pkg["description"]); d != "" {
			descParts = append(descParts, d)
		}
		if v := str(pkg["version"]); v != "" {
			descParts = append(descParts, "v"+v)
		}
		out = append(out, Suggestion{
			Name:        pkgName,
			Description: strings.Join(descParts, " · "),
			Suggestion: &registry.UpsertServerInput{
				Name: pkgName, Transport: storeTransport("stdio"), Command: strPtr("npx"),
				Args: strSlicePtr([]string{"-y", pkgName}), GatewayMode: boolPtr(true),
			},
			Source:   "npm",
			UseCount: num(asMap(obj["downloads"])["weekly"]),
		})
	}
	return out
}

// NpmManifestToSuggestion npm manifest（GET /<pkg>/latest）→ npx 建议；无 bin 的包无法经 npx 运行 → nil。
func NpmManifestToSuggestion(data map[string]any) *registry.UpsertServerInput {
	name := str(data["name"])
	bin := data["bin"]
	if name == "" || bin == nil {
		return nil
	}
	switch b := bin.(type) {
	case string:
		if b == "" {
			return nil
		}
	case map[string]any:
		if len(b) == 0 {
			return nil
		}
	default:
		return nil
	}
	return &registry.UpsertServerInput{
		Name: name, Transport: storeTransport("stdio"), Command: strPtr("npx"),
		Args: strSlicePtr([]string{"-y", name}), GatewayMode: boolPtr(true),
	}
}

// SmitheryToSuggestions Smithery 列表 → 统一走 Smithery 托管代理端点（网关模式 + Bearer 占位头）。
func SmitheryToSuggestions(data map[string]any) []Suggestion {
	out := []Suggestion{}
	for _, raw := range asSlice(data["servers"]) {
		s := asMap(raw)
		if s == nil {
			continue
		}
		qualified := str(s["qualifiedName"])
		if qualified == "" {
			continue
		}
		name := strings.TrimSpace(str(s["displayName"]))
		if name == "" {
			parts := strings.Split(qualified, "/")
			name = parts[len(parts)-1]
		}
		description := firstLine(str(s["description"]))
		headers := map[string]string{"Authorization": "Bearer {smithery_api_key}"}
		out = append(out, Suggestion{
			Name:        name,
			Description: description,
			UseCount:    num(s["useCount"]),
			Suggestion: &registry.UpsertServerInput{
				Name: name, Transport: storeTransport("http"),
				URL:     strPtr("https://server.smithery.ai/@" + qualified + "/mcp"),
				Headers: &headers, GatewayMode: boolPtr(true),
			},
			Source: "smithery",
		})
	}
	return out
}

// ModelscopeToSuggestions 魔搭 MCP 广场：中文名优先、本地 stdio 优先、热度取调用量。
func ModelscopeToSuggestions(data map[string]any) []Suggestion {
	out := []Suggestion{}
	dataM := asMap(data["Data"])
	mcp := asMap(asMap(dataM["McpServer"]))
	for _, raw := range asSlice(mcp["McpServers"]) {
		item := asMap(raw)
		if item == nil {
			continue
		}
		name := str(item["Name"])
		if name == "" {
			continue
		}
		if cn := strings.TrimSpace(str(item["ChineseName"])); cn != "" {
			name = cn
		}
		desc := firstNonEmpty(
			strings.TrimSpace(str(item["AbstractCN"])),
			strings.TrimSpace(str(item["Abstract"])),
			strings.TrimSpace(firstLine(firstNonEmpty(str(item["ReadmeCN"]), str(item["Readme"])))),
		)
		if len(desc) > 160 {
			desc = desc[:160]
		}
		callVolume := num(item["CallVolume"])
		stars := num(item["Stars"])
		var useCount *float64
		if callVolume != nil && *callVolume > 0 {
			useCount = callVolume
		} else if stars != nil && *stars > 0 {
			useCount = stars
		}
		// 传输配置优先级：本地 stdio（最可靠）→ ServerConfig 远程 → 托管 streamable-http/sse
		defs := mcpServerDefs(item["ServerConfig"])
		var stdioDef, remoteDef map[string]any
		for _, d := range defs {
			if str(d["command"]) != "" && stdioDef == nil {
				stdioDef = d
			}
			if str(d["url"]) != "" && remoteDef == nil {
				remoteDef = d
			}
		}
		httpDef := firstWithURL(item["StreamableHTTPServerConfig"])
		sseDef := firstWithURL(item["SSEServerConfig"])
		var suggestion *registry.UpsertServerInput
		switch {
		case str(stdioDef["command"]) != "":
			suggestion = &registry.UpsertServerInput{
				Name: name, Transport: storeTransport("stdio"), Command: strPtr(str(stdioDef["command"])),
				Args: optArgs(asSlice(stdioDef["args"])), GatewayMode: boolPtr(true),
			}
		case str(remoteDef["url"]) != "":
			transport := "http"
			if str(remoteDef["type"]) == "sse" {
				transport = "sse"
			}
			suggestion = &registry.UpsertServerInput{Name: name, Transport: storeTransport(transport), URL: strPtr(str(remoteDef["url"])), GatewayMode: boolPtr(true)}
		case httpDef != nil:
			suggestion = &registry.UpsertServerInput{Name: name, Transport: storeTransport("http"), URL: strPtr(str(httpDef["url"])), GatewayMode: boolPtr(true)}
		case sseDef != nil:
			suggestion = &registry.UpsertServerInput{Name: name, Transport: storeTransport("sse"), URL: strPtr(str(sseDef["url"])), GatewayMode: boolPtr(true)}
		}
		out = append(out, Suggestion{Name: name, Description: desc, UseCount: useCount, Suggestion: suggestion, Source: "modelscope"})
	}
	return out
}

func mcpServerDefs(v any) []map[string]any {
	out := []map[string]any{}
	for _, c := range asSlice(v) {
		cm := asMap(c)
		if cm == nil {
			continue
		}
		servers := asMap(cm["mcpServers"])
		if servers == nil {
			continue
		}
		for _, s := range servers {
			if m := asMap(s); m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

func firstWithURL(v any) map[string]any {
	for _, d := range mcpServerDefs(v) {
		if str(d["url"]) != "" {
			return d
		}
	}
	return nil
}

// BaiduMcpWorldToSuggestions 百度 MCP World：仅元数据无安装配置 → 建议为 nil 只做发现。
func BaiduMcpWorldToSuggestions(data map[string]any) []Suggestion {
	out := []Suggestion{}
	for _, it := range baiduItems(data) {
		serverName := str(it["serverName"])
		if serverName == "" {
			continue
		}
		desc := truncate(str(it["description"]), 160)
		var useCount *float64
		if star := num(it["star"]); star != nil && *star > 0 {
			useCount = star
		}
		out = append(out, Suggestion{Name: serverName, Description: desc, UseCount: useCount, Source: "baidu"})
	}
	return out
}

// baiduItems mcpList 三种形态：条目数组 / 含 {query,total,servers} wrapper 的数组 / wrapper 对象本身
func baiduItems(data map[string]any) []map[string]any {
	dataM := asMap(data["data"])
	raw := dataM["mcpList"]
	items := []map[string]any{}
	if list := asSlice(raw); list != nil {
		for _, x := range list {
			if m := asMap(x); m != nil {
				if _, isItem := m["serverName"]; isItem {
					items = append(items, m)
				} else if servers := asSlice(m["servers"]); servers != nil {
					for _, s := range servers {
						if sm := asMap(s); sm != nil {
							items = append(items, sm)
						}
					}
				}
			}
		}
	} else if raw != nil {
		for _, s := range asSlice(asMap(raw)["servers"]) {
			if sm := asMap(s); sm != nil {
				items = append(items, sm)
			}
		}
	}
	return items
}

// BaiduMcpWorldTotal 搜索命中总数：优先 mcpList 内层 total，否则全局收录数 data.count。
func BaiduMcpWorldTotal(data map[string]any) *float64 {
	dataM := asMap(data["data"])
	raw := dataM["mcpList"]
	if list := asSlice(raw); list != nil {
		for _, x := range list {
			if t := num(asMap(x)["total"]); t != nil {
				return t
			}
		}
	} else if raw != nil {
		if t := num(asMap(raw)["total"]); t != nil {
			return t
		}
	}
	return num(dataM["count"])
}

// ClineCatalogToSuggestions Cline 市场：install 模板两种形态——
// `<id> --transport http <url>` → 远程端点；`<id> -- npx -y <pkg>` → `--` 后为 stdio 命令。
func ClineCatalogToSuggestions(data map[string]any) []Suggestion {
	out := []Suggestion{}
	for _, raw := range asSlice(data["entries"]) {
		e := asMap(raw)
		if e == nil {
			continue
		}
		if str(e["type"]) != "mcp" {
			continue
		}
		name := str(e["name"])
		if name == "" {
			continue
		}
		install := asMap(e["install"])
		args := strSlice(install["args"])
		var suggestion *registry.UpsertServerInput
		tIdx := indexOf(args, "--transport")
		sepIdx := indexOf(args, "--")
		if tIdx != -1 {
			for _, a := range args[tIdx+2:] {
				if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
					suggestion = &registry.UpsertServerInput{Name: name, Transport: storeTransport("http"), URL: strPtr(a), GatewayMode: boolPtr(true)}
					break
				}
			}
		} else if sepIdx != -1 && sepIdx+1 < len(args) {
			rest := args[sepIdx+1:]
			suggestion = &registry.UpsertServerInput{
				Name: name, Transport: storeTransport("stdio"), Command: strPtr(rest[0]),
				GatewayMode: boolPtr(true),
			}
			if len(rest) > 1 {
				suggestion.Args = strSlicePtr(rest[1:])
			}
		}
		var requiredEnv []string
		for _, rawEnv := range asSlice(install["env"]) {
			em := asMap(rawEnv)
			if em == nil {
				continue
			}
			if b, ok := em["required"].(bool); ok && b && str(em["name"]) != "" {
				requiredEnv = append(requiredEnv, str(em["name"]))
			}
		}
		descParts := []string{}
		if d := firstNonEmpty(strings.TrimSpace(str(e["description"])), strings.TrimSpace(str(e["tagline"]))); d != "" {
			descParts = append(descParts, d)
		}
		if len(requiredEnv) > 0 {
			descParts = append(descParts, "需配置环境变量："+strings.Join(requiredEnv, ", "))
		}
		out = append(out, Suggestion{Name: name, Description: strings.Join(descParts, " · "), Suggestion: suggestion, Source: "cline"})
	}
	return out
}

func indexOf(list []string, target string) int {
	for i, v := range list {
		if v == target {
			return i
		}
	}
	return -1
}

// ObjectEntry 保序的对象键值对（TS Object.entries 语义：JSON 插入序）。
type ObjectEntry struct {
	Key   string
	Value map[string]any
}

// decodeObjectValues 保序解码顶层对象为键值对列表（Go map 无序，JSON 目录源的遍历序必须显式保留）。
func decodeObjectValues(raw []byte) []ObjectEntry {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		// 可能是 {servers: [...]} 包装，走宽松解码
		var wrapped map[string]any
		if json.Unmarshal(raw, &wrapped) == nil {
			if servers := asSlice(wrapped["servers"]); servers != nil {
				out := []ObjectEntry{}
				for i, s := range servers {
					if m := asMap(s); m != nil {
						out = append(out, ObjectEntry{Key: fmt.Sprint(i), Value: m})
					}
				}
				return out
			}
		}
		return nil
	}
	out := []ObjectEntry{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			break
		}
		key, _ := keyTok.(string)
		var v map[string]any
		if err := dec.Decode(&v); err != nil || v == nil {
			continue
		}
		out = append(out, ObjectEntry{Key: key, Value: v})
	}
	return out
}

// DockerCatalogToSuggestions Docker MCP 目录：server 类型 → docker run stdio（镜像含 digest 固定）。
func DockerCatalogToSuggestions(raw []byte) []Suggestion {
	out := []Suggestion{}
	// registry 对象可能嵌在顶层 registry 键下
	var topLevel map[string]any
	_ = json.Unmarshal(raw, &topLevel)
	var entriesRaw []byte
	if reg, ok := topLevel["registry"]; ok {
		entriesRaw, _ = json.Marshal(reg)
	} else {
		entriesRaw = raw
	}
	for _, entry := range decodeObjectValues(entriesRaw) {
		slug, e := entry.Key, entry.Value
		if e == nil {
			continue
		}
		if t := str(e["type"]); t != "" && t != "server" {
			continue
		}
		image := str(e["image"])
		if image == "" {
			continue
		}
		name := strings.TrimSpace(str(e["title"]))
		if name == "" {
			name = slug
		}
		out = append(out, Suggestion{
			Name:        name,
			Description: truncate(str(e["description"]), 160),
			Suggestion: &registry.UpsertServerInput{
				Name: name, Transport: storeTransport("stdio"), Command: strPtr("docker"),
				Args: strSlicePtr([]string{"run", "-i", "--rm", image}), GatewayMode: boolPtr(true),
			},
			Source: "docker",
		})
	}
	return out
}

// McpmRegistryToSuggestions mcpm.sh 聚合注册表：npm 安装优先、python（uvx）兜底、custom 占位跳过。
// 条目顺序 = 上游 JSON 插入序（slug→条目对象，保序解码）。
func McpmRegistryToSuggestions(raw []byte) []Suggestion {
	var probe struct {
		Servers []map[string]any `json:"servers"`
	}
	var entries []map[string]any
	if json.Unmarshal(raw, &probe) == nil && probe.Servers != nil {
		entries = probe.Servers
	} else {
		for _, entry := range decodeObjectValues(raw) {
			entries = append(entries, entry.Value)
		}
	}
	out := []Suggestion{}
	for _, e := range entries {
		name := str(e["name"])
		if name == "" {
			continue
		}
		if dn := strings.TrimSpace(str(e["display_name"])); dn != "" {
			name = dn
		}
		inst := asMap(e["installations"])
		var pick map[string]any
		if npm := asMap(inst["npm"]); npm != nil && str(npm["command"]) != "" {
			pick = npm
		} else if py := asMap(inst["python"]); py != nil && str(py["command"]) != "" {
			pick = py
		}
		var suggestion *registry.UpsertServerInput
		if pick != nil && str(pick["command"]) != "" {
			suggestion = &registry.UpsertServerInput{
				Name: name, Transport: storeTransport("stdio"), Command: strPtr(str(pick["command"])),
				GatewayMode: boolPtr(true),
			}
			if args := strSlice(pick["args"]); len(args) > 0 {
				suggestion.Args = strSlicePtr(args)
			}
			if env := asMap(pick["env"]); len(env) > 0 {
				m := map[string]string{}
				for k, v := range env {
					m[k] = str(v)
				}
				suggestion.Env = &m
			}
		}
		var required []string
		for k, v := range asMap(e["arguments"]) {
			if vm := asMap(v); vm != nil {
				if b, ok := vm["required"].(bool); ok && b {
					required = append(required, k)
				}
			}
		}
		sort.Strings(required)
		descParts := []string{}
		if d := strings.TrimSpace(str(e["description"])); d != "" {
			descParts = append(descParts, d)
		}
		if len(required) > 0 {
			descParts = append(descParts, "需配置："+strings.Join(required, ", "))
		}
		out = append(out, Suggestion{Name: name, Description: strings.Join(descParts, " · "), Suggestion: suggestion, Source: "mcpm"})
	}
	return out
}

// Dedupe 按展示名去重（大小写不敏感，保留先出现的）。
func Dedupe(list []Suggestion) []Suggestion {
	seen := map[string]struct{}{}
	out := []Suggestion{}
	for _, s := range list {
		key := strings.ToLower(s.Name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

// FilterByRelevance 相关性过滤 + 排序：官方 registry 偶发忽略 search 参数，
// 按关键词 token 过滤（name 命中排前、title/description 命中次之）。空关键词原样返回。
func FilterByRelevance(list []Suggestion, q string) []Suggestion {
	tokens := nonEmpty(strings.Split(strings.ToLower(strings.TrimSpace(q)), " "))
	if len(tokens) == 0 {
		return list
	}
	type scored struct {
		s    Suggestion
		rank int
	}
	var matched []scored
	for _, s := range list {
		name := strings.ToLower(s.Name)
		desc := strings.ToLower(s.Description)
		all := func(f func(string) bool) bool {
			for _, t := range tokens {
				if !f(t) {
					return false
				}
			}
			return true
		}
		some := func(f func(string) bool) bool {
			for _, t := range tokens {
				if f(t) {
					return true
				}
			}
			return false
		}
		switch {
		case all(func(t string) bool { return strings.Contains(name, t) }):
			matched = append(matched, scored{s, 0})
		case all(func(t string) bool { return strings.Contains(name+" "+desc, t) }):
			matched = append(matched, scored{s, 1})
		case some(func(t string) bool { return strings.Contains(name, t) || strings.Contains(desc, t) }):
			matched = append(matched, scored{s, 2})
		}
	}
	sort.SliceStable(matched, func(a, b int) bool {
		if matched[a].rank != matched[b].rank {
			return matched[a].rank < matched[b].rank
		}
		return useCountOf(matched[a].s) > useCountOf(matched[b].s)
	})
	out := make([]Suggestion, 0, len(matched))
	for _, m := range matched {
		out = append(out, m.s)
	}
	return out
}

// SortByPopularity 按热度排序（useCount 降序；无热度数据沉底但保持原相对顺序）。
func SortByPopularity(list []Suggestion) []Suggestion {
	out := make([]Suggestion, len(list))
	copy(out, list)
	sort.SliceStable(out, func(a, b int) bool {
		return useCountOf(out[a]) > useCountOf(out[b])
	})
	return out
}

func useCountOf(s Suggestion) float64 {
	if s.UseCount != nil {
		return *s.UseCount
	}
	return 0
}

func nonEmpty(list []string) []string {
	out := []string{}
	for _, v := range list {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
