// Package tui 终端面板（VENU-16）：exe 直接运行查看管理面板，替代"本地 HTTP + 浏览器"。
// 数据通道与 Web UI 完全同源——daemon REST API（环回 + Bearer），避免第二套业务逻辑；
// daemon 双模式与 tray 一致：未运行 = 本进程自持有（退出即停），已运行 = 挂远端。
package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/hxgh776/beaver/internal/daemon"
	"github.com/hxgh776/beaver/internal/gateway"
	"github.com/hxgh776/beaver/internal/registry"
	"github.com/hxgh776/beaver/internal/skills"
	"github.com/hxgh776/beaver/internal/store"
	"github.com/hxgh776/beaver/internal/syncengine"
)

// APIError daemon 返回 {"error": msg, "code": ...}；status 非 2xx 时统一转此类型。
type APIError struct {
	Status  int
	Message string
	Code    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	return e.Message
}

// —— 响应 DTO（字段名 1:1 对齐 daemon 输出；领域结构直接复用 internal 包） —— //

type StatusInfo struct {
	Version        string               `json:"version"`
	UptimeSec      int                  `json:"uptimeSec"`
	HomeDir        string               `json:"homeDir"`
	Port           int                  `json:"port"`
	ConfiguredPort int                  `json:"configuredPort"`
	Gateway        daemon.GatewayStatus `json:"gateway"`
	ServerCount    int                  `json:"serverCount"`
	BindingCount   int                  `json:"bindingCount"`
	GroupCount     int                  `json:"groupCount"`
	ResyncPending  bool                 `json:"resyncPending"`
	AuthRequired   bool                 `json:"authRequired"`
}

type RuntimeTool struct {
	Name    string  `json:"name"`
	Role    string  `json:"role"`
	Found   bool    `json:"found"`
	Version *string `json:"version,omitempty"`
}

type AgentInfo struct {
	AgentType       string   `json:"agentType"`
	DisplayName     string   `json:"displayName"`
	Detected        bool     `json:"detected"`
	ConfigPaths     []string `json:"configPaths"`
	Transports      []string `json:"transports"`
	DirectServerIds []string `json:"directServerIds"`
	GroupIds        []string `json:"groupIds"`
	BoundServerIds  []string `json:"boundServerIds"`
	SyncState       *struct {
		KeyStates []struct {
			Key  string `json:"key"`
			Hash string `json:"hash"`
		} `json:"keyStates"`
		UpdatedAt string `json:"updatedAt"`
	} `json:"syncState"`
}

type GatewayInfo struct {
	Running   bool                     `json:"running"`
	Port      *int                     `json:"port,omitempty"`
	Upstreams []gateway.UpstreamSnapshot `json:"upstreams"`
}

type ToolInfo struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Enabled     bool    `json:"enabled"`
}

type CallRecord struct {
	Ts         string  `json:"ts"`
	Scope      string  `json:"scope"`
	ServerID   string  `json:"serverId"`
	Tool       string  `json:"tool"`
	Ok         bool    `json:"ok"`
	DurationMs float64 `json:"durationMs"`
	Error      *string `json:"error,omitempty"`
}

type ImportGroup struct {
	Fingerprint    string `json:"fingerprint"`
	AlreadyImported bool  `json:"alreadyImported"`
	Candidates     []struct {
		AgentType string `json:"agentType"`
		Key       string `json:"key"`
	} `json:"candidates"`
	Suggested struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
	} `json:"suggested"`
}

type SettingsInfo struct {
	Port         int    `json:"port"`
	LogLevel     string `json:"logLevel"`
	AuthRequired bool   `json:"authRequired"`
	HasOnboarded bool   `json:"hasOnboarded"`
}

type AutostartInfo struct {
	Installed bool   `json:"installed"`
	CmdLine   string `json:"cmdLine,omitempty"`
}

type BackupFileInfo struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
}

type BackupGroup struct {
	AgentType string           `json:"agentType"`
	Files     []BackupFileInfo `json:"files"`
}

type BackupGroups struct {
	Groups        []BackupGroup    `json:"groups"`
	ConfigBackups []BackupFileInfo `json:"configBackups"`
}

type RegistrySuggestion struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Suggestion  *registry.UpsertServerInput `json:"suggestion"`
	Source      string                      `json:"source,omitempty"`
	UseCount    *float64                    `json:"useCount,omitempty"`
}

type RegistrySearchResult struct {
	Servers    []RegistrySuggestion `json:"servers"`
	Source     string               `json:"source,omitempty"`
	Total      int                  `json:"total,omitempty"`
	TotalPages int                  `json:"totalPages,omitempty"`
	HasMore    bool                 `json:"hasMore,omitempty"`
	Notes      []string             `json:"notes,omitempty"`
}

type SkillSearchResult struct {
	Query  string `json:"query"`
	Count  int    `json:"count"`
	Skills []struct {
		SkillID  string  `json:"skillId"`
		Name     string  `json:"name"`
		Installs float64 `json:"installs"`
		Repo     string  `json:"repo"`
		Branch   string  `json:"branch"`
	} `json:"skills"`
}

// —— Client —— //

type Client struct {
	base  string
	token string
	hc    *http.Client
}

func NewClient(base, token string) *Client {
	return &Client{
		base:  base,
		token: token,
		hc:    &http.Client{Timeout: 300 * time.Second},
	}
}

func (c *Client) Base() string { return c.base }

// do 单次请求；out 非 nil 时解码响应。非 2xx → *APIError。
func (c *Client) do(method, pathname string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+pathname, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("daemon 请求失败（%s %s）: %w", method, pathname, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		apiErr := &APIError{Status: res.StatusCode, Message: res.Status}
		var probe struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(data, &probe) == nil && probe.Error != "" {
			apiErr.Message = probe.Error
			apiErr.Code = probe.Code
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("响应解析失败（%s %s）: %w", method, pathname, err)
	}
	return nil
}

// —— 各端点（页面按需调用） —— //

func (c *Client) Status() (*StatusInfo, error) {
	var out StatusInfo
	if err := c.do("GET", "/api/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Agents(refresh bool) ([]AgentInfo, error) {
	path := "/api/agents"
	if refresh {
		path += "?refresh=1"
	}
	var out struct {
		Agents []AgentInfo `json:"agents"`
	}
	if err := c.do("GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

func (c *Client) Environment() ([]RuntimeTool, error) {
	var out struct {
		Tools []RuntimeTool `json:"tools"`
	}
	if err := c.do("GET", "/api/environment", nil, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (c *Client) Servers() ([]*store.ServerDef, error) {
	var out struct {
		Servers []*store.ServerDef `json:"servers"`
	}
	if err := c.do("GET", "/api/servers", nil, &out); err != nil {
		return nil, err
	}
	return out.Servers, nil
}

func (c *Client) CreateServer(in map[string]any) (*store.ServerDef, error) {
	var out struct {
		Server *store.ServerDef `json:"server"`
	}
	if err := c.do("POST", "/api/servers", in, &out); err != nil {
		return nil, err
	}
	return out.Server, nil
}

func (c *Client) PatchServer(id string, in map[string]any) (*store.ServerDef, error) {
	var out struct {
		Server *store.ServerDef `json:"server"`
	}
	if err := c.do("PATCH", "/api/servers/"+id, in, &out); err != nil {
		return nil, err
	}
	return out.Server, nil
}

func (c *Client) DeleteServer(id string) error {
	return c.do("DELETE", "/api/servers/"+id, nil, nil)
}

func (c *Client) UpstreamControl(id, action string) error {
	var out map[string]any
	return c.do("POST", "/api/servers/"+id+"/"+action, nil, &out)
}

func (c *Client) ServerTools(id string, refresh bool) ([]ToolInfo, error) {
	path := "/api/servers/" + id + "/tools"
	if refresh {
		path += "?refresh=1"
	}
	var out struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := c.do("GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (c *Client) InvokeTool(id, tool string, args map[string]any) (any, error) {
	var out struct {
		Result any `json:"result"`
	}
	if err := c.do("POST", "/api/servers/"+id+"/invoke", map[string]any{"tool": tool, "arguments": args}, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func (c *Client) UpstreamLogs(id string) ([]string, error) {
	var out struct {
		Lines []string `json:"lines"`
	}
	if err := c.do("GET", "/api/servers/"+id+"/logs", nil, &out); err != nil {
		return nil, err
	}
	return out.Lines, nil
}

func (c *Client) Groups() ([]store.ServerGroup, error) {
	var out struct {
		Groups []store.ServerGroup `json:"groups"`
	}
	if err := c.do("GET", "/api/groups", nil, &out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

func (c *Client) SaveGroup(id, name string, serverIds, agentTypes []string) error {
	body := map[string]any{"name": name, "serverIds": serverIds, "agentTypes": agentTypes}
	if id != "" {
		return c.do("PATCH", "/api/groups/"+id, body, nil)
	}
	return c.do("POST", "/api/groups", body, nil)
}

func (c *Client) DeleteGroup(id string) error {
	return c.do("DELETE", "/api/groups/"+id, nil, nil)
}

func (c *Client) GroupServer(id, serverId string, on bool) error {
	if on {
		return c.do("PUT", "/api/groups/"+id+"/servers/"+serverId, nil, nil)
	}
	return c.do("DELETE", "/api/groups/"+id+"/servers/"+serverId, nil, nil)
}

func (c *Client) GroupAgent(id, agentType string, on bool) error {
	if on {
		return c.do("PUT", "/api/groups/"+id+"/agents/"+agentType, nil, nil)
	}
	return c.do("DELETE", "/api/groups/"+id+"/agents/"+agentType, nil, nil)
}

func (c *Client) Binding(agentType, serverId string, on bool) error {
	if on {
		return c.do("PUT", "/api/bindings/"+agentType+"/"+serverId, nil, nil)
	}
	return c.do("DELETE", "/api/bindings/"+agentType+"/"+serverId, nil, nil)
}

func (c *Client) Sync(dryRun bool, resolutions map[string]string) (*syncengine.Report, error) {
	var out struct {
		Report *syncengine.Report `json:"report"`
	}
	body := map[string]any{"dryRun": dryRun}
	if len(resolutions) > 0 {
		body["resolutions"] = resolutions
	}
	if err := c.do("POST", "/api/sync", body, &out); err != nil {
		return nil, err
	}
	return out.Report, nil
}

func (c *Client) ImportPreview() ([]ImportGroup, error) {
	var out struct {
		Groups []ImportGroup `json:"groups"`
	}
	if err := c.do("GET", "/api/import/preview", nil, &out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

func (c *Client) Import(fingerprints []string) (int, error) {
	var out struct {
		Added []map[string]any `json:"added"`
	}
	if err := c.do("POST", "/api/import", map[string]any{"fingerprints": fingerprints}, &out); err != nil {
		return 0, err
	}
	return len(out.Added), nil
}

func (c *Client) Gateway() (*GatewayInfo, error) {
	var out GatewayInfo
	if err := c.do("GET", "/api/gateway", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GatewayStart() (*daemon.GatewayStatus, error) {
	var out daemon.GatewayStatus
	if err := c.do("POST", "/api/gateway/start", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GatewayStop() (*daemon.GatewayStatus, error) {
	var out daemon.GatewayStatus
	if err := c.do("POST", "/api/gateway/stop", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Calls() ([]CallRecord, error) {
	var out struct {
		Calls []CallRecord `json:"calls"`
	}
	if err := c.do("GET", "/api/logs/calls", nil, &out); err != nil {
		return nil, err
	}
	return out.Calls, nil
}

func (c *Client) Settings() (*SettingsInfo, error) {
	var out SettingsInfo
	if err := c.do("GET", "/api/settings", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PatchSettings(fields map[string]any) error {
	return c.do("PATCH", "/api/settings", fields, nil)
}

func (c *Client) Token() (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do("GET", "/api/settings/token", nil, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

func (c *Client) RotateToken() (string, bool, error) {
	var out struct {
		Token          string `json:"token"`
		ResyncRequired bool   `json:"resyncRequired"`
	}
	if err := c.do("POST", "/api/settings/token/rotate", nil, &out); err != nil {
		return "", false, err
	}
	return out.Token, out.ResyncRequired, nil
}

func (c *Client) Autostart() (*AutostartInfo, error) {
	var out AutostartInfo
	if err := c.do("GET", "/api/autostart", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SetAutostart(installed bool) (*AutostartInfo, error) {
	var out AutostartInfo
	if err := c.do("PUT", "/api/autostart", map[string]any{"installed": installed}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Backups() (*BackupGroups, error) {
	var out BackupGroups
	if err := c.do("GET", "/api/backups", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RestoreBackup(body map[string]any) error {
	return c.do("POST", "/api/backups/restore", body, nil)
}

func (c *Client) OpenDataDir() error {
	return c.do("POST", "/api/settings/open-dir", nil, nil)
}

func (c *Client) RegistrySearch(query, source string, limit, offset int) (*RegistrySearchResult, error) {
	var out RegistrySearchResult
	path := fmt.Sprintf("/api/registry/search?q=%s&source=%s&limit=%d&offset=%d", urlQueryEscape(query), urlQueryEscape(source), limit, offset)
	if err := c.do("GET", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Skills() ([]skills.WithStatus, string, error) {
	var out struct {
		Skills   []skills.WithStatus `json:"skills"`
		SsotDir  string              `json:"ssotDir"`
	}
	if err := c.do("GET", "/api/skills", nil, &out); err != nil {
		return nil, "", err
	}
	return out.Skills, out.SsotDir, nil
}

func (c *Client) SkillSearch(query string, limit, offset int) (*SkillSearchResult, error) {
	var out SkillSearchResult
	path := fmt.Sprintf("/api/skills/search?q=%s&limit=%d&offset=%d", urlQueryEscape(query), limit, offset)
	if err := c.do("GET", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SkillInstall(repo, skill, branch string, enableFor []string) (*skills.WithStatus, error) {
	var out struct {
		Skill *skills.WithStatus `json:"skill"`
	}
	body := map[string]any{"repo": repo, "enableFor": enableFor}
	if skill != "" {
		body["skill"] = skill
	}
	if branch != "" {
		body["branch"] = branch
	}
	if err := c.do("POST", "/api/skills/install", body, &out); err != nil {
		return nil, err
	}
	return out.Skill, nil
}

func (c *Client) SkillInstallCollection(repo, branch string, enableFor []string) (*skills.CollectionInstallReport, error) {
	var out skills.CollectionInstallReport
	body := map[string]any{"repo": repo, "enableFor": enableFor}
	if branch != "" {
		body["branch"] = branch
	}
	if err := c.do("POST", "/api/skills/install-collection", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SkillInstallZip(contentBase64 string, collection bool, enableFor []string) (*skills.WithStatus, *skills.CollectionInstallReport, error) {
	body := map[string]any{"contentBase64": contentBase64, "enableFor": enableFor, "collection": collection}
	if collection {
		var report skills.CollectionInstallReport
		if err := c.do("POST", "/api/skills/install-zip", body, &report); err != nil {
			return nil, nil, err
		}
		return nil, &report, nil
	}
	var out struct {
		Skill *skills.WithStatus `json:"skill"`
	}
	if err := c.do("POST", "/api/skills/install-zip", body, &out); err != nil {
		return nil, nil, err
	}
	return out.Skill, nil, nil
}

func (c *Client) SkillToggle(id, agentType string, enabled bool) (*skills.ToggleResult, error) {
	var out skills.ToggleResult
	if err := c.do("POST", "/api/skills/"+id+"/toggle", map[string]any{"agentType": agentType, "enabled": enabled}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SkillUpdates() (*skills.UpdateReport, error) {
	var out skills.UpdateReport
	if err := c.do("GET", "/api/skills/updates", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SkillUpdate(id string) (*skills.WithStatus, error) {
	var out struct {
		Skill *skills.WithStatus `json:"skill"`
	}
	if err := c.do("POST", "/api/skills/"+id+"/update", nil, &out); err != nil {
		return nil, err
	}
	return out.Skill, nil
}

func (c *Client) SkillUninstall(id string) (*skills.UninstallResult, error) {
	var out skills.UninstallResult
	if err := c.do("DELETE", "/api/skills/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
