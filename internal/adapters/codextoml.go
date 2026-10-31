// Codex CLI 适配器（config.toml，容器 [mcp_servers.*]）。
// 对应 TS 侧 packages/core/src/adapters/codex-toml.ts，行为 1:1 对齐。
//
// 写入采用"分段文本编辑"而非整文件重写（R2）：
//   - 按 TOML 表头把文件切成 前导区 + 段列表；
//   - 只摘除/替换本工具管辖的 [mcp_servers.<key>] 及其子表段；
//   - 其余字节原样保留（用户的注释、顺序、非托管段完全无损）；
//   - 新段由受限发射器序列化后统一追加到文件末尾。
package adapters

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	toml "github.com/BurntSushi/toml"

	"github.com/hxgh776/beaver/internal/coreerr"
	"github.com/hxgh776/beaver/internal/fsutil"
	"github.com/hxgh776/beaver/internal/jsonv"
	"github.com/hxgh776/beaver/internal/store"
)

type CodexTomlAdapter struct {
	paths []string
}

func NewCodexTomlAdapter(paths []string) *CodexTomlAdapter {
	return &CodexTomlAdapter{paths: paths}
}

func (a *CodexTomlAdapter) Type() string        { return store.AgentCodex }
func (a *CodexTomlAdapter) DisplayName() string { return "Codex CLI" }
func (a *CodexTomlAdapter) Transports() []store.Transport {
	return []store.Transport{store.TransportStdio}
}

func (a *CodexTomlAdapter) CandidatePaths() []string { return a.paths }

func (a *CodexTomlAdapter) NormalizeEntry(raw *jsonv.Value) *RawServerEntry {
	if raw == nil || !raw.IsObject() {
		return nil
	}
	r := raw.Object()
	if cmd := optString(r, "command"); cmd != nil {
		return &RawServerEntry{
			Transport: store.TransportStdio,
			Command:   cmd,
			Args:      toStringArray(r, "args"),
			Env:       toStringRecord(r, "env"),
		}
	}
	if url := optString(r, "url"); url != nil {
		return &RawServerEntry{Transport: store.TransportHTTP, URL: url, Headers: toStringRecord(r, "headers")}
	}
	return nil
}

func (a *CodexTomlAdapter) Read(file string) ([]NamedEntry, error) {
	text, ok, err := fsutil.ReadTextIfExists(file)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []NamedEntry{}, nil
	}
	parsed, err := a.parseOrThrow(text, file)
	if err != nil {
		return nil, err
	}
	out := []NamedEntry{}
	servers := nativeObject(parsed, "mcp_servers")
	if servers == nil {
		return out, nil
	}
	for _, key := range servers.Object().Keys() {
		val, _ := servers.Object().Get(key)
		if normalized := a.NormalizeEntry(val); normalized != nil {
			out = append(out, NamedEntry{Key: key, Entry: *normalized})
		}
	}
	return out, nil
}

func (a *CodexTomlAdapter) Apply(file string, writes []FragmentWrite, options ApplyOptions) (ApplyResult, error) {
	text, ok, err := fsutil.ReadTextIfExists(file)
	if err != nil {
		return ApplyResult{}, err
	}
	sourceText := ""
	if ok {
		sourceText = text
	}
	var parsed *jsonv.Value
	if strings.TrimSpace(sourceText) == "" {
		parsed = jsonv.NewObject()
	} else {
		if parsed, err = a.parseOrThrow(sourceText, file); err != nil {
			return ApplyResult{}, err
		}
	}
	var container *jsonv.Value
	if c := nativeObject(parsed, "mcp_servers"); c != nil {
		container = shallowClone(c)
	} else {
		container = jsonv.NewObject()
	}

	currentValues := map[string]*jsonv.Value{}
	for _, k := range container.Object().Keys() {
		v, _ := container.Object().Get(k)
		currentValues[k] = v
	}
	resolutions := options.Resolutions
	if resolutions == nil {
		resolutions = map[string]ConflictResolution{}
	}
	plan, conflicts, err := PlanChanges(planParams{
		CurrentValues: currentValues,
		Writes:        writes,
		Previous:      options.Previous,
		Resolutions:   resolutions,
		Denormalize:   func(entry *RawServerEntry) (*jsonv.Value, error) { return a.denormalize(entry) },
	})
	if err != nil {
		return ApplyResult{}, err
	}
	changes := make([]ApplyChange, 0, len(plan))
	for _, p := range plan {
		changes = append(changes, ApplyChange{Key: p.Key, Action: p.Action})
	}
	mutations := []PlannedChange{}
	for _, p := range plan {
		if p.Action != "none" {
			mutations = append(mutations, p)
		}
	}

	if options.DryRun {
		projected := map[string]*jsonv.Value{}
		for k, v := range currentValues {
			projected[k] = v
		}
		for _, m := range mutations {
			if m.Action == "upsert" {
				projected[m.Key] = m.Desired
			} else {
				delete(projected, m.Key)
			}
		}
		return ApplyResult{
			Changes:   changes,
			KeyStates: FinalizeKeyStates(plan, projected, options.Previous),
			Conflicts: conflicts,
		}, nil
	}
	if len(mutations) == 0 {
		return ApplyResult{
			Changes:   changes,
			KeyStates: FinalizeKeyStates(plan, currentValues, options.Previous),
			Conflicts: conflicts,
		}, nil
	}

	if options.Backup != nil {
		if err := options.Backup(); err != nil {
			return ApplyResult{}, err
		}
	}

	managedKeys := map[string]struct{}{}
	for _, p := range plan {
		managedKeys[p.Key] = struct{}{}
	}
	rest := StripManagedSections(sourceText, managedKeys)
	appended := []string{}
	for _, p := range plan {
		if p.Action == "upsert" {
			appended = append(appended, strings.TrimRight(emitTomlServerSection(p.Key, p.Desired), "\n\t "))
		}
	}
	newText := appendSections(rest, appended)
	// S0：发布前落意图，发布/回滚后清账
	if options.Journal != nil {
		var pre *string
		if ok {
			pre = &text
		}
		if err := options.Journal.Record(file, pre, newText); err != nil {
			return ApplyResult{}, err
		}
	}
	if err := fsutil.AtomicWriteFile(file, newText); err != nil {
		return ApplyResult{}, err
	}

	// 校验：非托管区字节级一致 + 管辖键解析结果与目标一致
	verified := false
	reread, rereadOk, err := fsutil.ReadTextIfExists(file)
	if err == nil && rereadOk {
		restOriginal := StripManagedSections(sourceText, managedKeys)
		restNew := StripManagedSections(reread, managedKeys)
		if restOriginal == restNew {
			if reparsed, perr := a.parseOrThrow(reread, file); perr == nil {
				reparsedServers := nativeObject(reparsed, "mcp_servers")
				verified = func() bool {
					for _, p := range plan {
						var after *jsonv.Value
						var afterExists bool
						if reparsedServers != nil {
							after, afterExists = reparsedServers.Object().Get(p.Key)
						}
						switch {
						case p.Action == "remove":
							if afterExists {
								return false
							}
						case p.Action == "none" && p.Desired == nil:
							cur, curExists := currentValues[p.Key]
							if afterExists != curExists {
								return false
							}
							if afterExists && !jsonvEqualValues(after, cur) {
								return false
							}
						default:
							if jsonvEqualValues(after, p.Desired) {
								continue
							}
							cur, curExists := currentValues[p.Key]
							if curExists && jsonvEqualValues(after, cur) {
								continue
							}
							return false
						}
					}
					return true
				}()
			}
		}
	}
	if !verified {
		if !ok {
			_ = os.Remove(file)
		} else {
			if err := fsutil.AtomicWriteFile(file, text); err != nil {
				return ApplyResult{}, err
			}
		}
		if options.Journal != nil {
			_ = options.Journal.Complete(file) // 已回滚到写前状态，意图清账
		}
		return ApplyResult{}, &coreerr.WriteVerificationError{FilePath: file}
	}
	if options.Journal != nil {
		if err := options.Journal.Complete(file); err != nil {
			return ApplyResult{}, err
		}
	}

	finalParsed, err := a.parseOrThrow(reread, file)
	if err != nil {
		return ApplyResult{}, err
	}
	finalValues := map[string]*jsonv.Value{}
	if c := nativeObject(finalParsed, "mcp_servers"); c != nil {
		for _, k := range c.Object().Keys() {
			v, _ := c.Object().Get(k)
			finalValues[k] = v
		}
	}
	return ApplyResult{
		Changes:   changes,
		KeyStates: FinalizeKeyStates(plan, finalValues, options.Previous),
		Conflicts: conflicts,
	}, nil
}

func (a *CodexTomlAdapter) parseOrThrow(text, file string) (*jsonv.Value, error) {
	var parsed map[string]any
	if _, err := toml.Decode(text, &parsed); err != nil {
		return nil, &coreerr.MalformedConfigError{FilePath: file, Cause: err}
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	return jsonv.ToGo(parsed), nil
}

// DenormalizeEntry 实现 Adapter 接口。
func (a *CodexTomlAdapter) DenormalizeEntry(entry *RawServerEntry) (*jsonv.Value, error) {
	return a.denormalize(entry)
}

func (a *CodexTomlAdapter) denormalize(entry *RawServerEntry) (*jsonv.Value, error) {
	// codex 原生仅 stdio；http 由同步引擎在直连分发前过滤，此处兜底转 stdio 不允许
	if entry.Transport != store.TransportStdio {
		return nil, fmt.Errorf("codex 直连仅支持 stdio 传输（http 类 server 请使用网关模式）")
	}
	o := jsonv.NewObject()
	if entry.Command != nil {
		o.Set("command", jsonv.NewString(*entry.Command))
	}
	if len(entry.Args) > 0 {
		arr := jsonv.NewArray()
		for _, arg := range entry.Args {
			arr.Append(jsonv.NewString(arg))
		}
		o.Set("args", arr)
	}
	if len(entry.Env) > 0 {
		o.Set("env", stringRecord(entry.Env))
	}
	return o, nil
}

func nativeObject(v *jsonv.Value, key string) *jsonv.Value {
	if v == nil {
		return nil
	}
	child, ok := v.Object().Get(key)
	if !ok || !child.IsObject() {
		return nil
	}
	return child
}

// —— TOML 分段编辑器 —— //

type tomlSection struct {
	// headerLine 表头原始行（含 [[ ]] 形式）
	headerLine string
	// name 表名（点分小写原样）
	name  string
	lines []string
}

var tomlHeaderRe = regexp.MustCompile(`^\s*\[\s*\[?\s*([^\]"]+|"[^"]*")\s*\]?\s*\]\s*(#.*)?$`)

func splitTomlSections(text string) (string, []tomlSection) {
	lines := strings.Split(text, "\n")
	preamble := []string{}
	sections := []tomlSection{}
	var current *tomlSection
	for _, line := range lines {
		m := tomlHeaderRe.FindStringSubmatch(line)
		if m != nil {
			name := strings.TrimSpace(m[1])
			name = strings.TrimSuffix(strings.TrimPrefix(name, `"`), `"`)
			sections = append(sections, tomlSection{headerLine: line, name: name, lines: []string{line}})
			current = &sections[len(sections)-1]
		} else if current != nil {
			current.lines = append(current.lines, line)
		} else {
			preamble = append(preamble, line)
		}
	}
	return strings.Join(preamble, "\n"), sections
}

func StripManagedSections(text string, managedKeys map[string]struct{}) string {
	preamble, sections := splitTomlSections(text)
	kept := []tomlSection{}
	for _, s := range sections {
		if !isManagedSection(s.name, managedKeys) {
			kept = append(kept, s)
		}
	}
	parts := []string{}
	if strings.TrimSpace(preamble) != "" {
		parts = append(parts, preamble)
	}
	for _, s := range kept {
		parts = append(parts, strings.Join(s.lines, "\n"))
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.Join(parts, "\n")
	// replace(/\n*$/, '\n')：把尾部连续换行折叠为单个换行
	out = strings.TrimRight(out, "\n") + "\n"
	return out
}

func isManagedSection(sectionName string, managedKeys map[string]struct{}) bool {
	for key := range managedKeys {
		prefix := "mcp_servers." + key
		if sectionName == prefix || strings.HasPrefix(sectionName, prefix+".") {
			return true
		}
	}
	return false
}

func appendSections(text string, sectionBlocks []string) string {
	if len(sectionBlocks) == 0 {
		return text
	}
	base := strings.TrimRight(text, "\n")
	body := strings.Join(sectionBlocks, "\n\n")
	if base == "" {
		return body + "\n"
	}
	return base + "\n\n" + body + "\n"
}

// emitTomlServerSection 受限形状发射器：仅处理 mcp_servers 条目的
// string / []string / map[string]string 三种值形态（对应 TS 侧 smol-toml
// 序列化新段的角色；验证以结构等价为准，不追求与 smol-toml 逐字节一致）。
func emitTomlServerSection(key string, desired *jsonv.Value) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[mcp_servers.%s]\n", key)
	if desired != nil && desired.IsObject() {
		o := desired.Object()
		for _, k := range o.Keys() {
			v, _ := o.Get(k)
			switch v.Kind() {
			case jsonv.KindString:
				fmt.Fprintf(&b, "%s = %s\n", k, tomlBasicString(v.String()))
			case jsonv.KindArray:
				parts := make([]string, 0, len(v.Array()))
				for _, e := range v.Array() {
					parts = append(parts, tomlBasicString(scalarToString(e)))
				}
				fmt.Fprintf(&b, "%s = [%s]\n", k, strings.Join(parts, ", "))
			case jsonv.KindObject:
				if v.Object().Len() == 0 {
					continue
				}
				fmt.Fprintf(&b, "\n[mcp_servers.%s.%s]\n", key, k)
				for _, ek := range v.Object().Keys() {
					ev, _ := v.Object().Get(ek)
					if ev.Kind() == jsonv.KindString {
						fmt.Fprintf(&b, "%s = %s\n", ek, tomlBasicString(ev.String()))
					}
				}
			}
		}
	}
	return b.String()
}

// tomlBasicString TOML basic string 转义：\ " 与控制字符。
func tomlBasicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\f':
			b.WriteString(`\f`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < 0x20:
			fmt.Fprintf(&b, `\u%04X`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
