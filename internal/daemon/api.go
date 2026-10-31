package daemon

// 管理 API 路由（对应 TS packages/server/src/api.ts，响应 JSON 字段名 1:1）。
// 延后项（M2）：/api/registry/search（九源搜索，core 未移植）、/api/skills*（skills 服务未移植）。

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/hxgh776/beaver/internal/adapters"
	"github.com/hxgh776/beaver/internal/registry"
	"github.com/hxgh776/beaver/internal/secretbox"
	"github.com/hxgh776/beaver/internal/skills"
	"github.com/hxgh776/beaver/internal/store"
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// 与 TS zod enum 保持一致（zcode/workbuddy 暂不在分组可选列表）。
var groupAgentTypes = []string{
	store.AgentClaudeCode, store.AgentCodex, store.AgentCursor, store.AgentTrae,
	store.AgentOpencode, store.AgentClaudeDesktop, store.AgentWindsurf, store.AgentGeminiCLI,
}

// serverInputRaw API 请求体的原始形态（字段缺失 = nil）。
type serverInputRaw struct {
	ID            string                         `json:"id"`
	Name          *string                        `json:"name"`
	Transport     *string                        `json:"transport"`
	Command       *string                        `json:"command"`
	Args          *[]string                      `json:"args"`
	Env           *map[string]string             `json:"env"`
	Cwd           *string                        `json:"cwd"`
	URL           *string                        `json:"url"`
	Headers       *map[string]string             `json:"headers"`
	GatewayMode   *bool                          `json:"gatewayMode"`
	Enabled       *bool                          `json:"enabled"`
	ToolOverrides *map[string]store.ToolOverride `json:"toolOverrides"`
	Concurrency   *int                           `json:"concurrency"`
}

// 表单字段的中文显示名（Issue-2：校验错误对用户友好）。
var fieldZH = map[string]string{
	"id": "ID", "name": "名称", "transport": "传输类型", "command": "命令", "args": "参数",
	"env": "环境变量", "cwd": "工作目录", "url": "URL", "headers": "请求头",
	"gatewayMode": "网关模式", "enabled": "启用", "toolOverrides": "工具开关",
	"serverIds": "包含的 server", "agentTypes": "关联的 agent", "concurrency": "并发数",
}

func (raw *serverInputRaw) validate(full bool) string {
	var errs []string
	if full {
		if raw.Name == nil || *raw.Name == "" {
			errs = append(errs, "名称：不能为空")
		}
		if raw.Transport == nil {
			errs = append(errs, "传输类型：类型不正确")
		} else {
			switch *raw.Transport {
			case "stdio", "http", "sse":
			default:
				errs = append(errs, "传输类型：格式不正确")
			}
		}
	} else {
		if raw.Name != nil && *raw.Name == "" {
			errs = append(errs, "名称：不能为空")
		}
		if raw.Transport != nil {
			switch *raw.Transport {
			case "stdio", "http", "sse":
			default:
				errs = append(errs, "传输类型：格式不正确")
			}
		}
	}
	if raw.ID != "" && !idRe.MatchString(raw.ID) {
		errs = append(errs, "ID：格式不正确")
	}
	if raw.Concurrency != nil && (*raw.Concurrency < 1 || *raw.Concurrency > 16) {
		errs = append(errs, "并发数：超出范围（1-16）")
	}
	if len(errs) == 0 {
		return ""
	}
	return errs[0] // 与 TS 一致逐条校验，这里返回首条
}

// toUpsertInput 转换为注册表输入（full=false 时跳过必需字段存在性检查，由调用方合并缺省）。
func (raw *serverInputRaw) toUpsertInput() registry.UpsertServerInput {
	in := registry.UpsertServerInput{
		ID:            raw.ID,
		Env:           raw.Env,
		Cwd:           raw.Cwd,
		Headers:       raw.Headers,
		GatewayMode:   raw.GatewayMode,
		Enabled:       raw.Enabled,
		ToolOverrides: raw.ToolOverrides,
		Concurrency:   raw.Concurrency,
	}
	if raw.Name != nil {
		in.Name = *raw.Name
	}
	if raw.Transport != nil {
		in.Transport = store.Transport(*raw.Transport)
	}
	in.Command = raw.Command
	in.Args = raw.Args
	in.URL = raw.URL
	return in
}

// crossCheck 创建/整体更新：传输类型与必需字段匹配（TS superRefine）。
func crossCheck(in *registry.UpsertServerInput) string {
	switch in.Transport {
	case store.TransportStdio:
		if in.Command == nil || *in.Command == "" {
			return "命令：stdio 传输需要 command"
		}
	case store.TransportHTTP:
		if in.URL == nil || *in.URL == "" {
			return "URL：http 传输需要 url"
		}
	}
	return ""
}

func boolPtr(b bool) *bool { return &b }

func strPtr(s string) *string { return &s }

// decryptForDisplay env/headers 存时加密、读时解密（S4）：返回脱敏前的展示副本。
func decryptForDisplay(def *store.ServerDef) *store.ServerDef {
	out := *def
	if def.Env != nil {
		dec, err := secretbox.DecryptRecord(*def.Env)
		if err == nil {
			out.Env = &dec
		} else {
			cp := make(map[string]string, len(*def.Env))
			for k, v := range *def.Env {
				cp[k] = v
			}
			out.Env = &cp
		}
	}
	if def.Headers != nil {
		dec, err := secretbox.DecryptRecord(*def.Headers)
		if err == nil {
			out.Headers = &dec
		} else {
			cp := make(map[string]string, len(*def.Headers))
			for k, v := range *def.Headers {
				cp[k] = v
			}
			out.Headers = &cp
		}
	}
	return &out
}

// encryptRawInput env/headers 存前加密（S4）。nil = 缺省（保持缺省语义）。
func encryptRawInput(raw *serverInputRaw) error {
	if raw.Env != nil {
		enc, err := secretbox.EncryptRecord(*raw.Env)
		if err != nil {
			return err
		}
		raw.Env = &enc
	}
	if raw.Headers != nil {
		enc, err := secretbox.EncryptRecord(*raw.Headers)
		if err != nil {
			return err
		}
		raw.Headers = &enc
	}
	return nil
}

func registerAPIRoutes(rt *Router, d *Daemon) {
	save := func(ctx *RequestContext) bool {
		if err := d.SaveConfig(); err != nil {
			SendError(ctx.Res, 500, fmt.Sprintf("保存配置失败: %v", err))
			return false
		}
		return true
	}

	rt.Get("/api/status", func(ctx *RequestContext) {
		_, _ = d.AgentDetections(false)
		gw := d.GatewayStatusFn()
		authRequired := d.Config.Settings.AuthRequired != nil && *d.Config.Settings.AuthRequired
		SendJSON(ctx.Res, 200, map[string]any{
			"version":        Version,
			"uptimeSec":      d.UptimeSec(),
			"homeDir":        d.Store.HomeDir,
			"port":           d.ActualPort(),
			"configuredPort": d.Config.Settings.Port,
			"gateway":        gw,
			"serverCount":    len(d.Config.Servers),
			"bindingCount":   len(d.Config.Bindings),
			"groupCount":     len(d.Config.Groups),
			"resyncPending":  d.Config.Settings.TokenRotatedAt != nil,
			"authRequired":   authRequired,
		})
	})

	// D7：公开探测端点（免鉴权）——UI 用它决定是否显示令牌门
	rt.Get("/api/auth/info", func(ctx *RequestContext) {
		authRequired := d.Config.Settings.AuthRequired != nil && *d.Config.Settings.AuthRequired
		SendJSON(ctx.Res, 200, map[string]any{"authRequired": authRequired})
	})

	// M2.2：本机 server 运行时探测
	rt.Get("/api/environment", func(ctx *RequestContext) {
		SendJSON(ctx.Res, 200, map[string]any{"tools": d.DetectEnvironment()})
	})

	rt.Get("/api/agents", func(ctx *RequestContext) {
		detections, err := d.AgentDetections(ctx.Query.Get("refresh") == "1")
		if err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		agents := make([]map[string]any, 0, len(detections))
		typeSet := map[string]struct{}{}
		for _, det := range detections {
			typeSet[det.AgentType] = struct{}{}
			direct := []string{}
			for _, b := range d.Config.Bindings {
				if b.AgentType == det.AgentType {
					direct = append(direct, b.ServerID)
				}
			}
			groupIDs := []string{}
			for _, g := range d.Config.Groups {
				for _, at := range g.AgentTypes {
					if at == det.AgentType {
						groupIDs = append(groupIDs, g.ID)
						break
					}
				}
			}
			item := map[string]any{
				"agentType":       det.AgentType,
				"displayName":     det.DisplayName,
				"detected":        det.Detected,
				"configPaths":     det.ConfigPaths,
				"transports":      det.Transports,
				"directServerIds": direct,
				"groupIds":        groupIDs,
			}
			if st, ok := d.Config.SyncState[det.AgentType]; ok {
				item["syncState"] = st
			} else {
				item["syncState"] = nil
			}
			bound := []string{}
			for id := range registry.EffectiveBoundIDs(d.Config, det.AgentType) {
				bound = append(bound, id)
			}
			item["boundServerIds"] = bound
			agents = append(agents, item)
		}
		types := make([]string, 0, len(typeSet))
		for t := range typeSet {
			types = append(types, t)
		}
		SendJSON(ctx.Res, 200, map[string]any{"agents": agents, "types": types})
	})

	// —— 注册表 CRUD ——
	rt.Get("/api/servers", func(ctx *RequestContext) {
		servers := make([]*store.ServerDef, 0, len(d.Config.Servers))
		for _, s := range d.Config.Servers {
			servers = append(servers, decryptForDisplay(s))
		}
		SendJSON(ctx.Res, 200, map[string]any{"servers": servers})
	})

	rt.Post("/api/servers", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		raw := parseServerInput(body)
		if msg := raw.validate(true); msg != "" {
			SendError(ctx.Res, 400, msg)
			return
		}
		if err := encryptRawInput(raw); err != nil {
			SendError(ctx.Res, 500, fmt.Sprintf("加密失败: %v", err))
			return
		}
		in := raw.toUpsertInput()
		if msg := crossCheck(&in); msg != "" {
			SendError(ctx.Res, 400, msg)
			return
		}
		def := registry.UpsertServer(d.Config, in)
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 201, map[string]any{"server": decryptForDisplay(def)})
	})

	rt.Get("/api/servers/:id", func(ctx *RequestContext) {
		def := findServer(d, ctx.Params["id"])
		if def == nil {
			SendError(ctx.Res, 404, "server 不存在")
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"server": decryptForDisplay(def)})
	})

	rt.Patch("/api/servers/:id", func(ctx *RequestContext) {
		id := ctx.Params["id"]
		def := findServer(d, id)
		if def == nil {
			SendError(ctx.Res, 404, "server 不存在")
			return
		}
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		raw := parseServerInput(body)
		if msg := raw.validate(false); msg != "" {
			SendError(ctx.Res, 400, msg)
			return
		}
		in := raw.toUpsertInput()
		in.ID = id
		if raw.Name == nil {
			in.Name = def.Name
		}
		if raw.Transport == nil {
			in.Transport = def.Transport
		}
		if err := encryptRawInput(raw); err != nil {
			SendError(ctx.Res, 500, fmt.Sprintf("加密失败: %v", err))
			return
		}
		registry.UpsertServer(d.Config, in)
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"server": decryptForDisplay(findServer(d, id))})
	})

	rt.Delete("/api/servers/:id", func(ctx *RequestContext) {
		id := ctx.Params["id"]
		if findServer(d, id) == nil {
			SendError(ctx.Res, 404, "server 不存在")
			return
		}
		registry.DeleteServer(d.Config, id)
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	// —— 绑定 ——
	rt.Put("/api/bindings/:agentType/:serverId", func(ctx *RequestContext) {
		if err := registry.SetBinding(d.Config, ctx.Params["serverId"], ctx.Params["agentType"]); err != nil {
			SendError(ctx.Res, 404, err.Error())
			return
		}
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	rt.Delete("/api/bindings/:agentType/:serverId", func(ctx *RequestContext) {
		registry.RemoveBinding(d.Config, ctx.Params["serverId"], ctx.Params["agentType"])
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	// —— 分组（批量分发：分组包含 server，agent 关联分组；另支持直连绑定） ——
	rt.Get("/api/groups", func(ctx *RequestContext) {
		groups := d.Config.Groups
		if groups == nil {
			groups = []store.ServerGroup{}
		}
		SendJSON(ctx.Res, 200, map[string]any{"groups": groups})
	})

	rt.Post("/api/groups", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		in, msg := parseGroupInput(body, false)
		if msg != "" {
			SendError(ctx.Res, 400, msg)
			return
		}
		group := registry.UpsertGroup(d.Config, in)
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 201, map[string]any{"group": group})
	})

	rt.Patch("/api/groups/:id", func(ctx *RequestContext) {
		id := ctx.Params["id"]
		var existing *store.ServerGroup
		for i := range d.Config.Groups {
			if d.Config.Groups[i].ID == id {
				existing = &d.Config.Groups[i]
				break
			}
		}
		if existing == nil {
			SendError(ctx.Res, 404, "分组不存在")
			return
		}
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		in, msg := parseGroupInput(body, true)
		if msg != "" {
			SendError(ctx.Res, 400, msg)
			return
		}
		in.ID = id
		if in.Name == "" {
			in.Name = existing.Name
		}
		if in.ServerIDs == nil {
			in.ServerIDs = existing.ServerIDs
		}
		if in.AgentTypes == nil {
			in.AgentTypes = existing.AgentTypes
		}
		updated := registry.UpsertGroup(d.Config, in)
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"group": updated})
	})

	rt.Delete("/api/groups/:id", func(ctx *RequestContext) {
		id := ctx.Params["id"]
		found := false
		for _, g := range d.Config.Groups {
			if g.ID == id {
				found = true
				break
			}
		}
		if !found {
			SendError(ctx.Res, 404, "分组不存在")
			return
		}
		registry.DeleteGroup(d.Config, id)
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	rt.Put("/api/groups/:id/servers/:serverId", func(ctx *RequestContext) {
		if _, err := registry.GroupAddServer(d.Config, ctx.Params["id"], ctx.Params["serverId"]); err != nil {
			SendError(ctx.Res, 404, err.Error())
			return
		}
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	rt.Delete("/api/groups/:id/servers/:serverId", func(ctx *RequestContext) {
		if _, err := registry.GroupRemoveServer(d.Config, ctx.Params["id"], ctx.Params["serverId"]); err != nil {
			SendError(ctx.Res, 404, err.Error())
			return
		}
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	rt.Put("/api/groups/:id/agents/:agentType", func(ctx *RequestContext) {
		if _, err := registry.GroupBindAgent(d.Config, ctx.Params["id"], ctx.Params["agentType"]); err != nil {
			SendError(ctx.Res, 404, err.Error())
			return
		}
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	rt.Delete("/api/groups/:id/agents/:agentType", func(ctx *RequestContext) {
		if _, err := registry.GroupUnbindAgent(d.Config, ctx.Params["id"], ctx.Params["agentType"]); err != nil {
			SendError(ctx.Res, 404, err.Error())
			return
		}
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	// —— 分发 ——
	rt.Post("/api/sync", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		dryRun, _ := body["dryRun"].(bool)
		resolutions := map[string]adapters.ConflictResolution{}
		if raw, ok := body["resolutions"].(map[string]any); ok {
			for k, v := range raw {
				s, _ := v.(string)
				if s == string(adapters.ResolutionOverride) {
					resolutions[k] = adapters.ResolutionOverride
				} else if s == string(adapters.ResolutionSkip) {
					resolutions[k] = adapters.ResolutionSkip
				}
			}
		}
		report, err := d.Sync(dryRun, resolutions)
		if err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		if !dryRun && !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"report": report})
	})

	// —— 导入 ——
	rt.Get("/api/import/preview", func(ctx *RequestContext) {
		groups, err := registry.ImportPreview(d.syncOpts.Agents)
		if err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		existing := map[string]struct{}{}
		for _, s := range d.Config.Servers {
			existing[registry.FingerprintOfServer(s)] = struct{}{}
		}
		out := make([]map[string]any, 0, len(groups))
		for _, g := range groups {
			_, imported := existing[g.Fingerprint]
			out = append(out, map[string]any{
				"fingerprint":     g.Fingerprint,
				"candidates":      g.Candidates,
				"suggested":       g.Suggested,
				"alreadyImported": imported,
			})
		}
		SendJSON(ctx.Res, 200, map[string]any{"groups": out})
	})

	rt.Post("/api/import", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		fingerprints := map[string]struct{}{}
		if rawList, ok := body["fingerprints"].([]any); ok {
			for _, f := range rawList {
				if s, ok := f.(string); ok {
					fingerprints[s] = struct{}{}
				}
			}
		}
		groups, err := registry.ImportPreview(d.syncOpts.Agents)
		if err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		selected := []registry.ImportCandidateGroup{}
		for _, g := range groups {
			if _, ok := fingerprints[g.Fingerprint]; ok {
				selected = append(selected, g)
			}
		}
		added := registry.ApplyImport(d.Config, selected, nil)
		if !save(ctx) {
			return
		}
		ids := make([]map[string]any, 0, len(added))
		for _, s := range added {
			ids = append(ids, map[string]any{"id": s.ID})
		}
		SendJSON(ctx.Res, 200, map[string]any{"added": ids})
	})

	// —— 进程与工具（网关注入实现；缺省 503） ——
	rt.Post("/api/servers/:id/start", func(ctx *RequestContext) { controlUpstream(d, ctx, "start") })
	rt.Post("/api/servers/:id/stop", func(ctx *RequestContext) { controlUpstream(d, ctx, "stop") })
	rt.Post("/api/servers/:id/restart", func(ctx *RequestContext) { controlUpstream(d, ctx, "restart") })

	rt.Get("/api/servers/:id/tools", func(ctx *RequestContext) {
		id := ctx.Params["id"]
		if d.ProbeTools == nil {
			SendError(ctx.Res, 503, "gateway 未启用", "GATEWAY_DOWN")
			return
		}
		tools := d.ProbeTools(id, ctx.Query.Get("refresh") == "1")
		if tools == nil {
			SendError(ctx.Res, 502, "无法连接上游或工具列表为空")
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"tools": tools})
	})

	rt.Get("/api/servers/:id/logs", func(ctx *RequestContext) {
		lines := []string{}
		if d.UpstreamLogs != nil {
			lines = d.UpstreamLogs(ctx.Params["id"])
		}
		SendJSON(ctx.Res, 200, map[string]any{"lines": lines})
	})

	// M3.1 调试台（S6）：不经 agent 直接调用工具
	rt.Post("/api/servers/:id/invoke", func(ctx *RequestContext) {
		id := ctx.Params["id"]
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		tool, _ := body["tool"].(string)
		if tool == "" {
			SendError(ctx.Res, 400, "缺少 tool")
			return
		}
		if findServer(d, id) == nil {
			SendError(ctx.Res, 404, "server 不存在")
			return
		}
		if d.InvokeTool == nil {
			SendError(ctx.Res, 503, "gateway 未启用", "GATEWAY_DOWN")
			return
		}
		args, _ := body["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		result, err := d.InvokeTool(id, tool, args)
		if err != nil {
			SendError(ctx.Res, 502, err.Error())
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"result": result})
	})

	// —— 日志 ——
	rt.Get("/api/logs/calls", func(ctx *RequestContext) {
		var calls any = []any{}
		if d.RecentCalls != nil {
			if v := d.RecentCalls(); v != nil {
				calls = v
			}
		}
		SendJSON(ctx.Res, 200, map[string]any{"calls": calls})
	})

	// —— 设置 ——
	rt.Get("/api/settings", func(ctx *RequestContext) {
		SendJSON(ctx.Res, 200, map[string]any{
			"port":         d.Config.Settings.Port,
			"logLevel":     d.Config.Settings.LogLevel,
			"authRequired": d.Config.Settings.AuthRequired != nil && *d.Config.Settings.AuthRequired,
			"hasOnboarded": d.Config.Settings.HasOnboarded != nil && *d.Config.Settings.HasOnboarded,
		})
	})

	rt.Get("/api/settings/token", func(ctx *RequestContext) {
		SendJSON(ctx.Res, 200, map[string]any{"token": d.Config.Settings.Token})
	})

	rt.Patch("/api/settings", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		if v, ok := body["port"]; ok && v != nil {
			port, ok := v.(float64)
			if !ok || port != float64(int(port)) || port < 1 || port > 65535 {
				SendError(ctx.Res, 400, "端口非法")
				return
			}
			d.Config.Settings.Port = int(port)
		}
		if v, ok := body["logLevel"]; ok && v != nil {
			level, _ := v.(string)
			switch level {
			case "debug", "info", "warn", "error":
				d.Config.Settings.LogLevel = level
				d.Logger.SetLevel(level)
			default:
				SendError(ctx.Res, 400, "日志级别非法")
				return
			}
		}
		if v, ok := body["hasOnboarded"]; ok && v != nil {
			b, ok := v.(bool)
			if !ok {
				SendError(ctx.Res, 400, "hasOnboarded 非法")
				return
			}
			d.Config.Settings.HasOnboarded = boolPtr(b)
		}
		if v, ok := body["authRequired"]; ok && v != nil {
			b, ok := v.(bool)
			if !ok {
				SendError(ctx.Res, 400, "authRequired 非法")
				return
			}
			d.Config.Settings.AuthRequired = boolPtr(b)
			// D7：开关切换后，已分发的网关配置形态（是否带凭证）与注册表不一致——提示重新同步
			if len(d.Config.Bindings) > 0 {
				d.Config.Settings.TokenRotatedAt = strPtr(nowISO())
			}
		}
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})

	rt.Post("/api/settings/token/rotate", func(ctx *RequestContext) {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			SendError(ctx.Res, 500, fmt.Sprintf("生成 token 失败: %v", err))
			return
		}
		d.Config.Settings.Token = hex.EncodeToString(buf)
		authOn := d.Config.Settings.AuthRequired != nil && *d.Config.Settings.AuthRequired
		resyncRequired := false
		// M2.4：仅在令牌启用时标记待重同步（关闭时网关条目不含 token，轮换无分发影响）
		if authOn && len(d.Config.Bindings) > 0 {
			d.Config.Settings.TokenRotatedAt = strPtr(nowISO())
			resyncRequired = true
		}
		if !save(ctx) {
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{
			"token":          d.Config.Settings.Token,
			"resyncRequired": resyncRequired,
		})
	})

	// —— 网关 ——
	rt.Get("/api/gateway", func(ctx *RequestContext) {
		gw := d.GatewayStatusFn()
		payload := map[string]any{"running": gw.Running}
		if gw.Port != nil {
			payload["port"] = *gw.Port
		}
		if hook := d.GatewaySnapshots; hook != nil {
			payload["upstreams"] = hook()
		} else {
			payload["upstreams"] = []any{}
		}
		SendJSON(ctx.Res, 200, payload)
	})

	rt.Post("/api/gateway/start", func(ctx *RequestContext) {
		if d.GatewayControl == nil {
			SendError(ctx.Res, 503, "gateway 模块未加载", "GATEWAY_DOWN")
			return
		}
		status, err := d.GatewayControl("start")
		if err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		SendJSON(ctx.Res, 200, status)
	})

	rt.Post("/api/gateway/stop", func(ctx *RequestContext) {
		if d.GatewayControl == nil {
			SendError(ctx.Res, 503, "gateway 模块未加载", "GATEWAY_DOWN")
			return
		}
		status, err := d.GatewayControl("stop")
		if err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		SendJSON(ctx.Res, 200, status)
	})

	// —— 九源 registry 搜索（映射逻辑在 internal/discovery，编排在 registry_search.go） ——
	registerRegistrySearchRoute(rt, d)

	// —— 设置页配套（autostart/备份恢复/打开数据目录） ——
	registerSettingsRoutes(rt, d)

	// —— daemon 生命周期（托盘远端模式退出用；token 鉴权由 handle 统一保障） ——
	rt.Post("/api/daemon/stop", func(ctx *RequestContext) {
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
		go func() {
			time.Sleep(200 * time.Millisecond) // 让响应先落盘
			d.Logger.Info("shutdown requested via /api/daemon/stop")
			_ = d.Stop()
			RemovePidFile(PidFilePath(d.Store.HomeDir)) // os.Exit 绕过托盘 onExit，这里自己清
			os.Exit(0)
		}()
	})

	// —— Skill 管理（S2，吸收自 cc-switch 的防御性设计；服务在 internal/skills） ——
	registerSkillRoutes(rt, d)
}

// registerSkillRoutes skills 全量路由（对齐 TS api.ts 的 skills 段响应形状）。
func registerSkillRoutes(rt *Router, d *Daemon) {
	asSkillAgents := func(list []string) []string {
		out := []string{}
		for _, a := range list {
			for _, valid := range skills.SkillAgentList {
				if a == valid {
					out = append(out, a)
					break
				}
			}
		}
		return out
	}

	rt.Get("/api/skills", func(ctx *RequestContext) {
		list, err := d.Skills.List()
		if err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"skills": list, "ssotDir": d.Skills.SkillsDir()})
	})

	rt.Get("/api/skills/search", func(ctx *RequestContext) {
		q := ctx.Query.Get("q")
		limit := clampInt(parseIntOr(ctx.Query.Get("limit"), 20), 1, 50)
		offset := maxInt(parseIntOr(ctx.Query.Get("offset"), 0), 0)
		result, err := d.Skills.Search(ctx.Req.Context(), q, limit, offset)
		if err != nil {
			SendError(ctx.Res, 502, err.Error(), "SKILLS_SH_DOWN")
			return
		}
		SendJSON(ctx.Res, 200, result)
	})

	rt.Post("/api/skills/install", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		repo, _ := body["repo"].(string)
		if len(repo) < 3 {
			SendError(ctx.Res, 400, "仓库坐标不能为空")
			return
		}
		skill, _ := body["skill"].(string)
		branch, _ := body["branch"].(string)
		enableFor := asSkillAgents(strList(body["enableFor"]))
		row, err := d.Skills.InstallFromRepo(ctx.Req.Context(), repo, skill, branch, enableFor)
		if err != nil {
			SendError(ctx.Res, 400, err.Error(), "SKILL_INSTALL_FAILED")
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"skill": row})
	})

	// 集合安装：一次下载仓库归档，收编全部含 SKILL.md 的目录；单技能冲突跳过不中断
	rt.Post("/api/skills/install-collection", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		repo, _ := body["repo"].(string)
		if len(repo) < 3 {
			SendError(ctx.Res, 400, "仓库坐标不能为空")
			return
		}
		branch, _ := body["branch"].(string)
		enableFor := asSkillAgents(strList(body["enableFor"]))
		report, err := d.Skills.InstallCollectionFromRepo(ctx.Req.Context(), repo, branch, enableFor)
		if err != nil {
			SendError(ctx.Res, 400, err.Error(), "SKILL_INSTALL_FAILED")
			return
		}
		SendJSON(ctx.Res, 200, report)
	})

	rt.Post("/api/skills/install-zip", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		contentB64, _ := body["contentBase64"].(string)
		if len(contentB64) < 8 {
			SendError(ctx.Res, 400, "缺少 ZIP 内容")
			return
		}
		zip, err := base64.StdEncoding.DecodeString(contentB64)
		if err != nil || len(zip) == 0 {
			SendError(ctx.Res, 400, "ZIP 内容为空")
			return
		}
		skill, _ := body["skill"].(string)
		enableFor := asSkillAgents(strList(body["enableFor"]))
		if b, ok := body["collection"].(bool); ok && b {
			report, err := d.Skills.InstallCollectionFromLocalZip(zip, enableFor)
			if err != nil {
				SendError(ctx.Res, 400, err.Error(), "SKILL_INSTALL_FAILED")
				return
			}
			SendJSON(ctx.Res, 200, map[string]any{"report": report})
			return
		}
		row, err := d.Skills.InstallFromLocalZip(zip, skill, enableFor)
		if err != nil {
			SendError(ctx.Res, 400, err.Error(), "SKILL_INSTALL_FAILED")
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"skill": row})
	})

	rt.Post("/api/skills/:id/toggle", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		agentType, _ := body["agentType"].(string)
		enabled, _ := body["enabled"].(bool)
		if agentType == "" {
			SendError(ctx.Res, 400, "agentType 不能为空")
			return
		}
		result, err := d.Skills.Toggle(ctx.Params["id"], agentType, enabled)
		if err != nil {
			SendError(ctx.Res, 400, err.Error(), "SKILL_TOGGLE_FAILED")
			return
		}
		SendJSON(ctx.Res, 200, result)
	})

	rt.Get("/api/skills/updates", func(ctx *RequestContext) {
		report, err := d.Skills.CheckUpdates(ctx.Req.Context())
		if err != nil {
			SendError(ctx.Res, 500, err.Error(), "SKILL_CHECK_FAILED")
			return
		}
		SendJSON(ctx.Res, 200, report)
	})

	rt.Post("/api/skills/:id/update", func(ctx *RequestContext) {
		row, err := d.Skills.Update(ctx.Req.Context(), ctx.Params["id"])
		if err != nil {
			SendError(ctx.Res, 400, err.Error(), "SKILL_UPDATE_FAILED")
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"skill": row})
	})

	rt.Delete("/api/skills/:id", func(ctx *RequestContext) {
		result, err := d.Skills.Uninstall(ctx.Params["id"])
		if err != nil {
			SendError(ctx.Res, 400, err.Error(), "SKILL_UNINSTALL_FAILED")
			return
		}
		SendJSON(ctx.Res, 200, result)
	})
}

func strList(v any) []string {
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

func controlUpstream(d *Daemon, ctx *RequestContext, action string) {
	id := ctx.Params["id"]
	if findServer(d, id) == nil {
		SendError(ctx.Res, 404, "server 不存在")
		return
	}
	if d.UpstreamControl == nil {
		SendError(ctx.Res, 503, "gateway 未启用", "GATEWAY_DOWN")
		return
	}
	r, err := d.UpstreamControl(id, action)
	if err != nil {
		SendError(ctx.Res, 500, err.Error())
		return
	}
	SendJSON(ctx.Res, 200, r)
}

func findServer(d *Daemon, id string) *store.ServerDef {
	for _, s := range d.Config.Servers {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func parseServerInput(body map[string]any) *serverInputRaw {
	data, _ := json.Marshal(body)
	var raw serverInputRaw
	_ = json.Unmarshal(data, &raw)
	return &raw
}

// parseGroupInput 校验分组输入；partial=true 时全部字段可选。
func parseGroupInput(body map[string]any, partial bool) (registry.UpsertGroupInput, string) {
	in := registry.UpsertGroupInput{}
	if name, ok := body["name"].(string); ok {
		in.Name = name
	}
	if list, ok := body["serverIds"].([]any); ok {
		in.ServerIDs = []string{}
		for _, v := range list {
			if s, ok := v.(string); ok {
				in.ServerIDs = append(in.ServerIDs, s)
			}
		}
	}
	if list, ok := body["agentTypes"].([]any); ok {
		in.AgentTypes = []string{}
		for _, v := range list {
			s, _ := v.(string)
			valid := false
			for _, a := range groupAgentTypes {
				if a == s {
					valid = true
					break
				}
			}
			if !valid {
				return in, "关联的 agent：类型不正确"
			}
			in.AgentTypes = append(in.AgentTypes, s)
		}
	}
	if !partial {
		if in.Name == "" {
			return in, "分组名不能为空"
		}
	} else if body["name"] != nil && in.Name == "" {
		return in, "分组名不能为空"
	}
	return in, ""
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
