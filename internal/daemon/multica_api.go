package daemon

// Multica agent 状态与连接设置 API（VENU-15 重构后）。
//
// multica 在 beaver 里是一个普通 agent（CLI 外置型）：MCP 分发走 /api/bindings +
// /api/sync（落在 workspace MCP 库），Skill 分发走 /api/skills/:id/toggle（推进
// workspace Skill 库）。beaver 不碰 Multica 平台内部把条目分给哪些 agent，
// 因此本文件只剩两项：
//
//   GET /api/agents/multica/state  agent 状态读回：连接诊断三层（CLI/登录/workspace）
//                                  + 远端库分区读回（分区独立报错，不互相拖垮）
//   PUT /api/multica/settings      连接设置保存（唯一写 settings.multica 的路径；
//                                  workspaceId 必须在真实列表内）
//
// 失败语义：所有远端失败归类为 *multica.Error（code+hint），可直接诊断。

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hxgh776/beaver/internal/multica"
	"github.com/hxgh776/beaver/internal/store"
)

// multicaSettings 当前连接设置（nil 配置 → 全缺省）。也是 MulticaAdapter 的动态源。
func (d *Daemon) multicaSettings() store.MulticaSettings {
	if d.Config == nil || d.Config.Settings.Multica == nil {
		return store.MulticaSettings{}
	}
	return *d.Config.Settings.Multica
}

// multicaCLI 按当前 settings.multica 构造调用配置（无缓存，设置变更即时生效）。
func (d *Daemon) multicaCLI() *multica.CLI {
	return multica.CLIFromSettings(d.multicaSettings())
}

// multicaCLIFunc 技能投影通道（SkillLibrarySink）的动态 CLI 源。
func (d *Daemon) multicaCLIFunc() *multica.CLI { return d.multicaCLI() }

// sectionError 分区错误（可诊断：code/message/hint）。
type sectionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func sectionErrorOf(err error) *sectionError {
	if err == nil {
		return nil
	}
	if me, ok := err.(*multica.Error); ok {
		return &sectionError{Code: string(me.Code), Message: me.Message, Hint: me.Hint}
	}
	return &sectionError{Code: string(multica.CodeUnknown), Message: err.Error()}
}

func registerMulticaRoutes(rt *Router, d *Daemon) {
	// —— agent 状态读回：连接诊断三层 + 远端库读回（库读回在连接就绪后进行） ——
	rt.Get("/api/agents/multica/state", func(ctx *RequestContext) {
		c := d.multicaCLI()
		cctx := ctx.Req.Context()
		res := map[string]any{
			"workspaceId": c.WorkspaceID,
			"workspaces":  []multica.Workspace{},
			"mcp":         map[string]any{"servers": []multica.MCPServer{}},
			"skills":      map[string]any{"skills": []multica.Skill{}},
		}
		exe, binErr := c.ResolveBinary()
		if binErr != nil {
			res["ok"] = false
			res["error"] = sectionErrorOf(binErr)
			SendJSON(ctx.Res, 200, res)
			return
		}
		res["cli"] = map[string]any{"found": true, "binary": exe}
		auth, authErr := c.AuthStatus(cctx)
		if authErr != nil {
			res["ok"] = false
			res["error"] = sectionErrorOf(authErr)
			SendJSON(ctx.Res, 200, res)
			return
		}
		res["auth"] = auth
		if !auth.LoggedIn {
			res["ok"] = false
			res["error"] = &sectionError{
				Code:    string(multica.CodeNotLoggedIn),
				Message: "multica CLI 未登录",
				Hint:    "在终端运行 multica login 完成登录后，点击刷新",
			}
			SendJSON(ctx.Res, 200, res)
			return
		}
		workspaces, wsErr := c.ListWorkspaces(cctx)
		if wsErr != nil {
			res["ok"] = false
			res["error"] = sectionErrorOf(wsErr)
			SendJSON(ctx.Res, 200, res)
			return
		}
		if len(workspaces) == 0 {
			res["ok"] = false
			res["error"] = &sectionError{
				Code:    string(multica.CodePermission),
				Message: "当前账号不属于任何 workspace",
				Hint:    "先在 Multica 上加入或创建 workspace",
			}
			SendJSON(ctx.Res, 200, res)
			return
		}
		res["workspaces"] = workspaces
		// 未显式选过 workspace 时仅回显建议值（GET 是只读探针，不写配置）；
		// 用户在设置里切换下拉或保存时才落盘
		if c.WorkspaceID == "" {
			res["workspaceId"] = workspaces[0].ID
		}
		// —— 远端库读回（multica 的「当前配置」；分区独立报错） ——
		ok := true
		servers, mcpErr := c.ListMCPServers(cctx)
		if mcpErr != nil {
			res["mcp"] = map[string]any{"error": sectionErrorOf(mcpErr)}
			ok = false
		} else {
			res["mcp"] = map[string]any{"servers": servers}
		}
		skills, skErr := c.ListSkills(cctx)
		if skErr != nil {
			res["skills"] = map[string]any{"error": sectionErrorOf(skErr)}
			ok = false
		} else {
			res["skills"] = map[string]any{"skills": skills}
		}
		res["ok"] = ok
		SendJSON(ctx.Res, 200, res)
	})

	// —— 连接设置保存（workspace 切换等；workspaceId 必须来自真实列表） ——
	rt.Put("/api/multica/settings", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		c := d.multicaCLI()
		if v, ok := body["binary"].(string); ok {
			c.Binary = strings.TrimSpace(v)
		}
		if v, ok := body["profile"].(string); ok {
			c.Profile = strings.TrimSpace(v)
		}
		if v, ok := body["serverUrl"].(string); ok {
			c.ServerURL = strings.TrimSpace(v)
		}
		if v, ok := body["workspaceId"].(string); ok {
			c.WorkspaceID = strings.TrimSpace(v)
		}
		if c.WorkspaceID != "" {
			// 校验目标 workspace 真实存在（防止手改 config 后面板静默指向不存在的地方）
			ws, err := c.ListWorkspaces(ctx.Req.Context())
			if err != nil {
				sendMulticaError(ctx.Res, err)
				return
			}
			found := false
			for _, w := range ws {
				if w.ID == c.WorkspaceID {
					found = true
					break
				}
			}
			if !found {
				SendError(ctx.Res, 400, "workspaceId 不在当前账号的 workspace 列表中", "INVALID_WORKSPACE")
				return
			}
		}
		d.Config.Settings.Multica = &store.MulticaSettings{
			Binary:      c.Binary,
			Profile:     c.Profile,
			ServerURL:   c.ServerURL,
			WorkspaceID: c.WorkspaceID,
		}
		if err := d.SaveConfig(); err != nil {
			SendError(ctx.Res, 500, fmt.Sprintf("保存配置失败: %v", err))
			return
		}
		// 连接设置变了 → 探测缓存失效（CLI 路径可能变）
		d.agentMu.Lock()
		d.agentCache = nil
		d.agentMu.Unlock()
		SendJSON(ctx.Res, 200, map[string]any{"ok": true, "settings": d.Config.Settings.Multica})
	})
}

// sendMulticaError 统一把 *multica.Error 映射到 HTTP 状态并附 code/hint。
func sendMulticaError(res http.ResponseWriter, err error) {
	if me, ok := err.(*multica.Error); ok {
		payload := map[string]any{"error": me.Message, "code": string(me.Code)}
		if me.Hint != "" {
			payload["hint"] = me.Hint
		}
		SendJSON(res, me.HTTPStatus(), payload)
		return
	}
	SendError(res, 500, err.Error())
}
