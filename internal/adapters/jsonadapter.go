// 通用 JSON 容器适配器：claude-code / cursor / trae / opencode / claude-desktop /
// windsurf / gemini-cli / zcode / workbuddy 共用。
// 对应 TS 侧 packages/core/src/adapters/json-adapter.ts，行为 1:1 对齐。
//
// 写入流程（C6 安全写入）：
// parse → 只改容器内管辖键 → 原子写 → 复读深比较校验（管辖子树之外全等）→ 失败即回滚并抛错。
package adapters

import (
	"os"

	"github.com/hxgh776/beaver/internal/coreerr"
	"github.com/hxgh776/beaver/internal/fsutil"
	"github.com/hxgh776/beaver/internal/jsonv"
	"github.com/hxgh776/beaver/internal/store"
)

type JsonAdapterOptions struct {
	Type        string
	DisplayName string
	Transports  []store.Transport
	Paths       []string
	// ContainerKey server 条目所在的容器键，如 mcpServers / mcp；支持点路径嵌套（如 ZCode 的 mcp.servers）
	ContainerKey string
	Normalize    func(raw *jsonv.Value) *RawServerEntry
	Denormalize  func(entry *RawServerEntry) (*jsonv.Value, error)
}

type JsonAgentAdapter struct {
	adapterType string
	displayName string
	transports  []store.Transport
	paths       []string
	segments    []string
	normalize   func(raw *jsonv.Value) *RawServerEntry
	denormalize func(entry *RawServerEntry) (*jsonv.Value, error)
}

func NewJsonAgentAdapter(opts JsonAdapterOptions) *JsonAgentAdapter {
	segs := splitDot(opts.ContainerKey)
	return &JsonAgentAdapter{
		adapterType: opts.Type,
		displayName: opts.DisplayName,
		transports:  opts.Transports,
		paths:       opts.Paths,
		segments:    segs,
		normalize:   opts.Normalize,
		denormalize: opts.Denormalize,
	}
}

func splitDot(s string) []string {
	out := []string{}
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(s[i])
	}
	out = append(out, cur)
	return out
}

func (a *JsonAgentAdapter) Type() string        { return a.adapterType }
func (a *JsonAgentAdapter) DisplayName() string { return a.displayName }
func (a *JsonAgentAdapter) Transports() []store.Transport {
	return a.transports
}
func (a *JsonAgentAdapter) CandidatePaths() []string { return a.paths }

func (a *JsonAgentAdapter) NormalizeEntry(raw *jsonv.Value) *RawServerEntry {
	return a.normalize(raw)
}

func (a *JsonAgentAdapter) DenormalizeEntry(entry *RawServerEntry) (*jsonv.Value, error) {
	return a.denormalize(entry)
}

type located struct {
	parent *jsonv.Value // object
	leaf   string
}

// locate 容器叶子所在的父对象与键名；中间层不是对象时返回 nil（读视为无容器，写则需先创建）。
func (a *JsonAgentAdapter) locate(record *jsonv.Value) *located {
	cur := record
	for i := 0; i < len(a.segments)-1; i++ {
		next, ok := cur.Object().Get(a.segments[i])
		if !ok {
			return nil
		}
		if !next.IsObject() {
			return nil
		}
		cur = next
	}
	return &located{parent: cur, leaf: a.segments[len(a.segments)-1]}
}

// ensureLocate 逐层创建中间对象并返回容器叶子位置（仅写路径使用）。
func (a *JsonAgentAdapter) ensureLocate(record *jsonv.Value) located {
	cur := record
	for i := 0; i < len(a.segments)-1; i++ {
		seg := a.segments[i]
		next, ok := cur.Object().Get(seg)
		if !ok || !next.IsObject() {
			fresh := jsonv.NewObject()
			cur.Object().Set(seg, fresh)
			cur = fresh
		} else {
			cur = next
		}
	}
	return located{parent: cur, leaf: a.segments[len(a.segments)-1]}
}

func (a *JsonAgentAdapter) containerOf(record *jsonv.Value) *jsonv.Value {
	loc := a.locate(record)
	if loc == nil {
		return nil
	}
	v, ok := loc.parent.Object().Get(loc.leaf)
	if !ok || !v.IsObject() {
		return nil
	}
	return v
}

func (a *JsonAgentAdapter) Read(file string) ([]NamedEntry, error) {
	text, ok, err := fsutil.ReadTextIfExists(file)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []NamedEntry{}, nil
	}
	obj, err := a.parseOrThrow(text, file)
	if err != nil {
		return nil, err
	}
	container := a.containerOf(obj)
	out := []NamedEntry{}
	if container == nil {
		return out, nil
	}
	for _, key := range container.Object().Keys() {
		val, _ := container.Object().Get(key)
		if normalized := a.normalize(val); normalized != nil {
			out = append(out, NamedEntry{Key: key, Entry: *normalized})
		}
	}
	return out, nil
}

func (a *JsonAgentAdapter) Apply(file string, writes []FragmentWrite, options ApplyOptions) (ApplyResult, error) {
	text, ok, err := fsutil.ReadTextIfExists(file)
	if err != nil {
		return ApplyResult{}, err
	}
	var obj *jsonv.Value
	if ok {
		obj, err = a.parseOrThrow(text, file)
		if err != nil {
			return ApplyResult{}, err
		}
	} else {
		obj = jsonv.NewObject()
	}
	// 中间层存在但不是纯对象（如 mcp: "str"）→ 形状未知，拒绝写入而非静默覆盖
	// （walk 是独立变量——根引用绝不能被中间层遍历覆盖）
	walk := obj
	for i := 0; i < len(a.segments)-1; i++ {
		next, exists := walk.Object().Get(a.segments[i])
		if !exists {
			break
		}
		if !next.IsObject() {
			return ApplyResult{}, &coreerr.MalformedConfigError{FilePath: file}
		}
		walk = next
	}
	existingContainer := a.containerOf(obj)
	leaf := a.locate(obj)
	var leafExists bool
	if leaf != nil {
		_, leafExists = leaf.parent.Object().Get(leaf.leaf)
	}
	containerExisted := leafExists
	if containerExisted && existingContainer == nil {
		return ApplyResult{}, &coreerr.MalformedConfigError{FilePath: file}
	}
	var container *jsonv.Value
	if existingContainer != nil {
		container = shallowClone(existingContainer)
	} else {
		container = jsonv.NewObject()
	}

	currentValues := map[string]*jsonv.Value{}
	if existingContainer != nil {
		for _, k := range existingContainer.Object().Keys() {
			v, _ := existingContainer.Object().Get(k)
			currentValues[k] = v
		}
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
		Denormalize:   a.denormalize,
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

	for _, m := range mutations {
		if m.Action == "upsert" {
			container.Object().Set(m.Key, m.Desired)
		} else {
			container.Object().Delete(m.Key)
		}
	}
	if container.Object().Len() > 0 || containerExisted {
		loc := a.ensureLocate(obj)
		loc.parent.Object().Set(loc.leaf, container)
	}

	newText := jsonv.StringifyPretty(obj) + "\n"
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

	// 复读校验：整体内容一致 + 屏蔽管辖子树后与写前全等
	verified := false
	reread, rereadOk, err := fsutil.ReadTextIfExists(file)
	if err == nil && rereadOk {
		if reparsed, perr := jsonv.Parse([]byte(reread)); perr == nil {
			if jsonvEqualValues(reparsed, obj) {
				var snapshot *jsonv.Value
				if ok {
					snapshot, perr = a.parseOrThrow(text, file)
					if perr != nil {
						snapshot = nil
					}
				} else {
					snapshot = jsonv.NewObject()
				}
				if snapshot != nil {
					verified = jsonvEqualValues(a.maskManaged(snapshot), a.maskManaged(reparsed))
				}
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

	finalContainer := a.containerOf(obj)
	finalValues := map[string]*jsonv.Value{}
	if finalContainer != nil {
		for _, k := range finalContainer.Object().Keys() {
			v, _ := finalContainer.Object().Get(k)
			finalValues[k] = v
		}
	}
	return ApplyResult{
		Changes:   changes,
		KeyStates: FinalizeKeyStates(plan, finalValues, options.Previous),
		Conflicts: conflicts,
	}, nil
}

func jsonvEqualValues(a, b *jsonv.Value) bool {
	return fsutil.StableStringify(a) == fsutil.StableStringify(b)
}

func shallowClone(obj *jsonv.Value) *jsonv.Value {
	n := jsonv.NewObject()
	for _, k := range obj.Object().Keys() {
		v, _ := obj.Object().Get(k)
		n.Set(k, v) // 浅拷贝：子值共享（TS {...container} 同语义）
	}
	return n
}

// maskManaged 深拷贝并移除管辖容器子树——剩余部分必须与写前全等。
// 写入可能新建中间层（如首次分发创建 mcp），摘除叶子后残留的空中间壳
// 两侧对称剪除，写前不含该中间层的快照才能与写后对齐。
func (a *JsonAgentAdapter) maskManaged(record *jsonv.Value) *jsonv.Value {
	clone := record.Clone()
	type link struct {
		parent *jsonv.Value
		leaf   string
	}
	chain := []link{}
	cur := clone
	for _, seg := range a.segments {
		chain = append(chain, link{parent: cur, leaf: seg})
		next, ok := cur.Object().Get(seg)
		if !ok || !next.IsObject() {
			break
		}
		cur = next
	}
	last := chain[len(chain)-1]
	last.parent.Object().Delete(last.leaf)
	for i := len(chain) - 2; i >= 0; i-- {
		node, ok := chain[i].parent.Object().Get(chain[i].leaf)
		if !ok || !node.IsObject() {
			break
		}
		if node.Object().Len() > 0 {
			break
		}
		chain[i].parent.Object().Delete(chain[i].leaf)
	}
	return clone
}

func (a *JsonAgentAdapter) parseOrThrow(text, file string) (*jsonv.Value, error) {
	parsed, err := jsonv.Parse([]byte(text))
	if err != nil || !parsed.IsObject() {
		return nil, &coreerr.MalformedConfigError{FilePath: file, Cause: err}
	}
	return parsed, nil
}

// —— 各 agent 共用的归一化器 ——（键序与 TS clean() 输出一致，不得改动）

func optString(r *jsonv.Object, key string) *string {
	if v, ok := r.Get(key); ok && v.Kind() == jsonv.KindString {
		s := v.String()
		return &s
	}
	return nil
}

func toStringArray(r *jsonv.Object, key string) []string {
	v, ok := r.Get(key)
	if !ok || v.Kind() != jsonv.KindArray {
		return nil
	}
	out := make([]string, 0, len(v.Array()))
	for _, e := range v.Array() {
		out = append(out, scalarToString(e))
	}
	return out
}

func toStringRecord(r *jsonv.Object, key string) map[string]string {
	v, ok := r.Get(key)
	if !ok || !v.IsObject() {
		return nil
	}
	out := map[string]string{}
	for _, k := range v.Object().Keys() {
		val, _ := v.Object().Get(k)
		if val.Kind() == jsonv.KindString {
			out[k] = val.String()
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scalarToString 对应 TS String() 强转。
func scalarToString(v *jsonv.Value) string {
	switch v.Kind() {
	case jsonv.KindString:
		return v.String()
	case jsonv.KindNumber:
		return jsonv.Canonical(v)
	case jsonv.KindBool:
		if v.Bool() {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func setIfNotNil(o *jsonv.Value, key string, m map[string]string) {
	if m != nil {
		o.Set(key, stringRecord(m))
	}
}

func setIfNotNilArr(o *jsonv.Value, key string, a []string) {
	if a != nil {
		arr := jsonv.NewArray()
		for _, s := range a {
			arr.Append(jsonv.NewString(s))
		}
		o.Set(key, arr)
	}
}

func stringRecord(m map[string]string) *jsonv.Value {
	o := jsonv.NewObject()
	for k, v := range m {
		o.Set(k, jsonv.NewString(v))
	}
	return o
}

func strPtr(s string) *string { return &s }

// NormalizeClaudeStyle claude-code 风格：{ command,args,env } 或 { type:'http'|'sse', url, headers }
func NormalizeClaudeStyle(raw *jsonv.Value) *RawServerEntry {
	if raw == nil || !raw.IsObject() {
		return nil
	}
	r := raw.Object()
	typeVal := ""
	if tv, ok := r.Get("type"); ok && tv.Kind() == jsonv.KindString {
		typeVal = tv.String()
	}
	if typeVal == "http" || typeVal == "sse" {
		url := optString(r, "url")
		if url == nil {
			return nil
		}
		return &RawServerEntry{Transport: store.Transport(typeVal), URL: url, Headers: toStringRecord(r, "headers")}
	}
	if url := optString(r, "url"); url != nil {
		if _, hasCommand := r.Get("command"); !hasCommand {
			return &RawServerEntry{Transport: store.TransportHTTP, URL: url, Headers: toStringRecord(r, "headers")}
		}
	}
	if cmd := optString(r, "command"); cmd != nil {
		return &RawServerEntry{
			Transport: store.TransportStdio,
			Command:   cmd,
			Args:      toStringArray(r, "args"),
			Env:       toStringRecord(r, "env"),
			Cwd:       optString(r, "cwd"),
		}
	}
	return nil
}

func DenormalizeClaudeStyle(entry *RawServerEntry) (*jsonv.Value, error) {
	o := jsonv.NewObject()
	if entry.Transport == store.TransportStdio {
		if entry.Command != nil {
			o.Set("command", jsonv.NewString(*entry.Command))
		}
		setIfNotNilArr(o, "args", entry.Args)
		setIfNotNil(o, "env", entry.Env)
		if entry.Cwd != nil {
			o.Set("cwd", jsonv.NewString(*entry.Cwd))
		}
		return o, nil
	}
	t := "http"
	if entry.Transport == store.TransportSSE {
		t = "sse"
	}
	o.Set("type", jsonv.NewString(t))
	if entry.URL != nil {
		o.Set("url", jsonv.NewString(*entry.URL))
	}
	setIfNotNil(o, "headers", entry.Headers)
	return o, nil
}

// NormalizeUrlOrCommand cursor / trae / windsurf 风格：无 type 字段的 { command,args,env } 或 { url, headers }
func NormalizeUrlOrCommand(raw *jsonv.Value) *RawServerEntry {
	if raw == nil || !raw.IsObject() {
		return nil
	}
	r := raw.Object()
	typeVal := ""
	if tv, ok := r.Get("type"); ok && tv.Kind() == jsonv.KindString {
		typeVal = tv.String()
	}
	if url := optString(r, "url"); url != nil {
		t := store.TransportHTTP
		if typeVal == "sse" {
			t = store.TransportSSE
		}
		return &RawServerEntry{Transport: t, URL: url, Headers: toStringRecord(r, "headers")}
	}
	if cmd := optString(r, "command"); cmd != nil {
		return &RawServerEntry{
			Transport: store.TransportStdio,
			Command:   cmd,
			Args:      toStringArray(r, "args"),
			Env:       toStringRecord(r, "env"),
		}
	}
	return nil
}

func DenormalizeUrlOrCommand(entry *RawServerEntry) (*jsonv.Value, error) {
	o := jsonv.NewObject()
	if entry.Transport == store.TransportStdio {
		if entry.Command != nil {
			o.Set("command", jsonv.NewString(*entry.Command))
		}
		setIfNotNilArr(o, "args", entry.Args)
		setIfNotNil(o, "env", entry.Env)
		return o, nil
	}
	if entry.URL != nil {
		o.Set("url", jsonv.NewString(*entry.URL))
	}
	setIfNotNil(o, "headers", entry.Headers)
	return o, nil
}

// NormalizeOpencode opencode 风格：{ type:'local', command:[...], environment } 或 { type:'remote', url }
func NormalizeOpencode(raw *jsonv.Value) *RawServerEntry {
	if raw == nil || !raw.IsObject() {
		return nil
	}
	r := raw.Object()
	tv, _ := r.Get("type")
	isRemote := tv != nil && tv.Kind() == jsonv.KindString && tv.String() == "remote"
	isLocal := tv != nil && tv.Kind() == jsonv.KindString && tv.String() == "local"
	cmdArr, hasCmdArr := r.Get("command")
	if isRemote {
		url := optString(r, "url")
		if url == nil {
			return nil
		}
		return &RawServerEntry{Transport: store.TransportHTTP, URL: url, Headers: toStringRecord(r, "headers")}
	}
	if isLocal && hasCmdArr && cmdArr.Kind() == jsonv.KindArray {
		if len(cmdArr.Array()) == 0 {
			return nil
		}
		cmd := scalarToString(cmdArr.Array()[0])
		args := make([]string, 0, len(cmdArr.Array())-1)
		for _, e := range cmdArr.Array()[1:] {
			args = append(args, scalarToString(e))
		}
		return &RawServerEntry{
			Transport: store.TransportStdio,
			Command:   &cmd,
			Args:      args,
			Env:       toStringRecord(r, "environment"),
		}
	}
	return nil
}

func DenormalizeOpencode(entry *RawServerEntry) (*jsonv.Value, error) {
	o := jsonv.NewObject()
	if entry.Transport == store.TransportStdio {
		o.Set("type", jsonv.NewString("local"))
		cmdArr := jsonv.NewArray()
		if entry.Command != nil {
			cmdArr.Append(jsonv.NewString(*entry.Command))
		}
		for _, a := range entry.Args {
			cmdArr.Append(jsonv.NewString(a))
		}
		o.Set("command", cmdArr)
		setIfNotNil(o, "environment", entry.Env)
		return o, nil
	}
	o.Set("type", jsonv.NewString("remote"))
	if entry.URL != nil {
		o.Set("url", jsonv.NewString(*entry.URL))
	}
	setIfNotNil(o, "headers", entry.Headers)
	return o, nil
}

// NormalizeGeminiStyle Gemini CLI 风格：{ command,args,env } / { httpUrl }（streamable http）/ { url }（sse）
func NormalizeGeminiStyle(raw *jsonv.Value) *RawServerEntry {
	if raw == nil || !raw.IsObject() {
		return nil
	}
	r := raw.Object()
	if url := optString(r, "httpUrl"); url != nil {
		return &RawServerEntry{Transport: store.TransportHTTP, URL: url, Headers: toStringRecord(r, "headers")}
	}
	if url := optString(r, "url"); url != nil {
		return &RawServerEntry{Transport: store.TransportSSE, URL: url, Headers: toStringRecord(r, "headers")}
	}
	if cmd := optString(r, "command"); cmd != nil {
		return &RawServerEntry{
			Transport: store.TransportStdio,
			Command:   cmd,
			Args:      toStringArray(r, "args"),
			Env:       toStringRecord(r, "env"),
			Cwd:       optString(r, "cwd"),
		}
	}
	return nil
}

func DenormalizeGeminiStyle(entry *RawServerEntry) (*jsonv.Value, error) {
	o := jsonv.NewObject()
	if entry.Transport == store.TransportStdio {
		if entry.Command != nil {
			o.Set("command", jsonv.NewString(*entry.Command))
		}
		setIfNotNilArr(o, "args", entry.Args)
		setIfNotNil(o, "env", entry.Env)
		if entry.Cwd != nil {
			o.Set("cwd", jsonv.NewString(*entry.Cwd))
		}
		return o, nil
	}
	if entry.Transport == store.TransportSSE {
		if entry.URL != nil {
			o.Set("url", jsonv.NewString(*entry.URL))
		}
		setIfNotNil(o, "headers", entry.Headers)
		return o, nil
	}
	if entry.URL != nil {
		o.Set("httpUrl", jsonv.NewString(*entry.URL))
	}
	setIfNotNil(o, "headers", entry.Headers)
	return o, nil
}

// NormalizeWorkbuddy 风格：{ type:'streamableHttp'|'sse', url, timeout?, disabled? } 或 { command,args,env }
func NormalizeWorkbuddy(raw *jsonv.Value) *RawServerEntry {
	if raw == nil || !raw.IsObject() {
		return nil
	}
	r := raw.Object()
	typeVal := ""
	if tv, ok := r.Get("type"); ok && tv.Kind() == jsonv.KindString {
		typeVal = tv.String()
	}
	if url := optString(r, "url"); url != nil {
		t := store.TransportHTTP
		if typeVal == "sse" {
			t = store.TransportSSE
		}
		return &RawServerEntry{Transport: t, URL: url, Headers: toStringRecord(r, "headers")}
	}
	if cmd := optString(r, "command"); cmd != nil {
		return &RawServerEntry{
			Transport: store.TransportStdio,
			Command:   cmd,
			Args:      toStringArray(r, "args"),
			Env:       toStringRecord(r, "env"),
		}
	}
	return nil
}

func DenormalizeWorkbuddy(entry *RawServerEntry) (*jsonv.Value, error) {
	o := jsonv.NewObject()
	if entry.Transport == store.TransportStdio {
		if entry.Command != nil {
			o.Set("command", jsonv.NewString(*entry.Command))
		}
		setIfNotNilArr(o, "args", entry.Args)
		setIfNotNil(o, "env", entry.Env)
		return o, nil
	}
	t := "streamableHttp"
	if entry.Transport == store.TransportSSE {
		t = "sse"
	}
	o.Set("type", jsonv.NewString(t))
	if entry.URL != nil {
		o.Set("url", jsonv.NewString(*entry.URL))
	}
	setIfNotNil(o, "headers", entry.Headers)
	return o, nil
}
