package daemon

// 九源 registry 搜索路由（对应 TS packages/server/src/api.ts 的 /api/registry/search 编排层）。
// 映射逻辑在 internal/discovery；这里只做外呼、翻页、回退链与目录缓存。
// 回退链：github → official → npm；smithery → npm；目录源（cline/docker/mcpm）无回退。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hxgh776/beaver/internal/discovery"
)

// 上游地址包级变量：测试用 httptest 覆盖。
var (
	officialRegistry = "https://registry.modelcontextprotocol.io"
	githubRegistry   = "https://api.mcp.github.com"
	smitheryRegistry = "https://registry.smithery.ai"
	npmHostOfficial  = "https://registry.npmjs.org"
	npmHostMirror    = "https://registry.npmmirror.com"
	modelscopeAPI    = "https://www.modelscope.cn/api/v1/dolphin/mcpServers"
	baiduMcpAPI      = "https://www.mcpworld.com/api/mcp-market/servers"
	clineCatalog     = "https://cline.github.io/marketplace/catalog.json"
	dockerCatalog    = "https://desktop.docker.com/mcp/catalog/v2/catalog.json"
	mcpmRegistry     = "https://mcpm.sh/api/servers.json"
)

const (
	defaultPageSize = 20
	// npm 单次搜索上限 250：拉满后本地按热度排序再切片
	popularityWindow = 250
	smitheryPage     = 100
	// 热度排序窗口：前 300 条内全局排序，更深页退回上游相关序
	smitheryWindow = 300
)

// npmHosts() 每次调用构造（地址变量可被测试覆盖，不能在 init 固化切片）。
func npmHosts() []string { return []string{npmHostOfficial, npmHostMirror} }

// 静态目录（Cline/Docker/mcpm 无服务端搜索）的进程内缓存：整包拉一次，TTL 内复用
var (
	catalogCacheMu sync.Mutex
	catalogCache   = map[string]catalogEntry{}
)

type catalogEntry struct {
	at   time.Time
	data json.RawMessage
}

type searchState struct {
	d         *Daemon
	res       http.ResponseWriter
	query     url.Values
	q         string
	limit     int
	offset    int
	failures  []string
	responded bool
}

func (s *searchState) sendJSON(status int, payload map[string]any) {
	if !s.responded {
		s.responded = true
		SendJSON(s.res, status, payload)
	}
}

func (s *searchState) fail(msg string) {
	s.failures = append(s.failures, msg)
}

// fetchJSON GET/PUT 一个 JSON 端点（超时可控），返回解码结果。
func fetchJSON(ctx context.Context, method, target string, body any, timeout time.Duration) (map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = strings.NewReader(string(data))
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, target, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("返回 %d", res.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// fetchRaw GET 一个 JSON 端点，返回未解码字节（保序解码用）。
func fetchRaw(ctx context.Context, method, target string, timeout time.Duration) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("返回 %d", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

// fetchCatalogRaw 整包目录拉取（TTL 10 分钟；attempts 允许 Docker CDN 偶发重置重试）。
// 返回原始字节：目录源（mcpm/Docker）的对象键序需要保序解码。
func (s *searchState) fetchCatalogRaw(ctx context.Context, target string, attempts int) (json.RawMessage, error) {
	catalogCacheMu.Lock()
	if hit, ok := catalogCache[target]; ok && time.Since(hit.at) < 10*time.Minute {
		catalogCacheMu.Unlock()
		return hit.data, nil
	}
	catalogCacheMu.Unlock()
	var lastErr error
	for i := 0; i < attempts; i++ {
		data, err := fetchRaw(ctx, http.MethodGet, target, 20*time.Second)
		if err == nil {
			catalogCacheMu.Lock()
			catalogCache[target] = catalogEntry{at: time.Now(), data: data}
			catalogCacheMu.Unlock()
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func paginated(payload map[string]any, servers []discovery.Suggestion, total *float64, offset, limit int) map[string]any {
	out := map[string]any{"servers": servers}
	for k, v := range payload {
		out[k] = v
	}
	out["servers"] = servers
	if total != nil {
		out["total"] = int(*total)
		out["totalPages"] = int(math.Ceil(*total / float64(limit)))
		out["hasMore"] = offset+len(servers) < int(*total)
	} else {
		out["hasMore"] = len(servers) >= limit
	}
	return out
}

// registerRegistrySearchRoute GET /api/registry/search
func registerRegistrySearchRoute(rt *Router, d *Daemon) {
	rt.Get("/api/registry/search", func(ctx *RequestContext) {
		s := &searchState{d: d, res: ctx.Res, query: ctx.Query}
		s.q = strings.TrimSpace(ctx.Query.Get("q"))
		s.limit = clampInt(parseIntOr(ctx.Query.Get("limit"), defaultPageSize), 1, 50)
		s.offset = maxInt(parseIntOr(ctx.Query.Get("offset"), 0), 0)
		source := ctx.Query.Get("source")
		if source == "" {
			source = "official"
		}
		goCtx := ctx.Req.Context()

		// —— npm 搜索（优先官方源、失败回退国内镜像；相关性过滤 + 按周下载热度排序） ——
		npmSearch := func() {
			if s.q == "" {
				return // npm 无浏览目录：空词直接返回，让上层兜底报错
			}
			for _, host := range npmHosts() {
				deep := s.offset+s.limit > popularityWindow
				size := minInt(maxInt(s.offset+s.limit, 50), popularityWindow)
				from := 0
				if deep {
					size = s.limit
					from = s.offset
				}
				target := fmt.Sprintf("%s/-/v1/search?text=%s&size=%d&from=%d", host, url.QueryEscape(s.q), size, from)
				data, err := fetchJSON(goCtx, http.MethodGet, target, nil, 10*time.Second)
				if err != nil {
					s.fail(fmt.Sprintf("npm(%s) 不可达: %v", host, truncateStr(err.Error(), 80)))
					continue
				}
				// 先相关性过滤再按热度排：npm 上游模糊匹配会混入大量高下载无关包
				candidates := discovery.FilterByRelevance(discovery.NpmSearchToSuggestions(data), s.q)
				ranked := discovery.SortByPopularity(candidates)
				fromIdx := s.offset - from
				if fromIdx < 0 {
					fromIdx = 0
				}
				if fromIdx > len(ranked) {
					fromIdx = len(ranked)
				}
				end := fromIdx + s.limit
				if end > len(ranked) {
					end = len(ranked)
				}
				servers := ranked[fromIdx:end]
				payload := map[string]any{"source": "npm", "notes": s.failures}
				s.sendJSON(200, paginated(payload, servers, numOf(data["total"]), s.offset, s.limit))
				return
			}
		}

		// —— Smithery（浅页多页合并按热度全局排序；深页退回上游相关序） ——
		smitherySearch := func() {
			var merged []map[string]any
			var total *float64
			listURL := func(page, pageSize int) string {
				q := ""
				if s.q != "" {
					q = "q=" + url.QueryEscape(s.q) + "&"
				}
				return fmt.Sprintf("%s/servers?%spage=%d&pageSize=%d", smitheryRegistry, q, page, pageSize)
			}
			if s.offset+s.limit <= smitheryWindow {
				pagesToFetch := minInt(ceilDiv(s.offset+s.limit, smitheryPage), smitheryWindow/smitheryPage)
				for p := 1; p <= pagesToFetch; p++ {
					data, err := fetchJSON(goCtx, http.MethodGet, listURL(p, smitheryPage), nil, 15*time.Second)
					if err != nil {
						s.fail(fmt.Sprintf("Smithery 返回: %v", err))
						break
					}
					merged = append(merged, asSliceAny(data["servers"])...)
					if pg := asMapAny(data["pagination"]); pg != nil {
						if t := numOf(pg["totalCount"]); t != nil {
							total = t
						}
						if tp := numOf(pg["totalPages"]); tp != nil && p >= int(*tp) {
							break
						}
					}
				}
			} else {
				page := s.offset/s.limit + 1
				data, err := fetchJSON(goCtx, http.MethodGet, listURL(page, s.limit), nil, 15*time.Second)
				if err != nil {
					s.fail(fmt.Sprintf("Smithery 返回: %v", err))
				} else {
					merged = asSliceAny(data["servers"])
					if pg := asMapAny(data["pagination"]); pg != nil {
						total = numOf(pg["totalCount"])
					}
				}
			}
			if len(merged) == 0 && total == nil {
				npmSearch()
				return
			}
			wrapped := map[string]any{"servers": toAnySlice(merged)}
			ranked := discovery.SortByPopularity(discovery.SmitheryToSuggestions(wrapped))
			var servers []discovery.Suggestion
			if s.offset+s.limit <= smitheryWindow {
				servers = sliceRange(ranked, s.offset, s.offset+s.limit)
			} else {
				servers = sliceRange(ranked, 0, s.limit)
			}
			s.sendJSON(200, paginated(map[string]any{"source": "smithery", "notes": s.failures}, servers, total, s.offset, s.limit))
		}

		// —— npm manifest 精确回查（官方条目缺安装配置时按名补 npx 建议） ——
		npmManifestBackfill := func(names []string) map[string]*discovery.Suggestion {
			out := map[string]*discovery.Suggestion{}
			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, name := range names {
				wg.Add(1)
				go func(name string) {
					defer wg.Done()
					for _, host := range npmHosts() {
						target := host + "/" + url.PathEscape(name) + "/latest"
						manifest, err := fetchJSON(goCtx, http.MethodGet, target, nil, 8*time.Second)
						if err != nil {
							continue // 网络失败 → 换下一个镜像
						}
						if sug := discovery.NpmManifestToSuggestion(manifest); sug != nil {
							mu.Lock()
							out[name] = &discovery.Suggestion{Name: name, Suggestion: sug}
							mu.Unlock()
						}
						return // 拿到 manifest 即止（镜像内容一致）
					}
				}(name)
			}
			wg.Wait()
			return out
		}

		// —— 官方 registry / GitHub 镜像（同构 schema：offset 翻页 + 相关性过滤 + 按名去重；失败回退） ——
		officialBase := ""
		if d.Config.Settings.RegistryBaseURL != nil {
			officialBase = strings.TrimSpace(*d.Config.Settings.RegistryBaseURL)
		}
		if officialBase == "" {
			officialBase = officialRegistry
		}
		officialLikeSearch := func(base, label, source string, onFail func()) {
			// 官方是 cursor 翻页：一次拉到 offset+limit 再切片；搜索时多拉留出去重/过滤余量
			headroom := 0
			if s.q != "" {
				headroom = s.limit * 2
			}
			fetchLimit := minInt(s.offset+s.limit+headroom, 200)
			searchPart := ""
			if s.q != "" {
				searchPart = "search=" + url.QueryEscape(s.q) + "&"
			}
			target := fmt.Sprintf("%s/v0/servers?%slimit=%d", base, searchPart, fetchLimit)
			data, err := fetchJSON(goCtx, http.MethodGet, target, nil, 15*time.Second)
			if err != nil {
				s.fail(fmt.Sprintf("%s 不可达: %v", label, truncateStr(err.Error(), 80)))
				onFail()
				return
			}
			var entries []map[string]any
			for _, raw := range asSliceAny(data["servers"]) {
				if m := asMapAny(raw); m != nil {
					if inner := asMapAny(m["server"]); inner != nil {
						entries = append(entries, inner)
					} else {
						entries = append(entries, m)
					}
				}
			}
			candidates := make([]discovery.Suggestion, 0, len(entries))
			for _, e := range entries {
				sug := discovery.RegistryEntryToServerInput(e)
				sug.Source = source
				candidates = append(candidates, sug)
			}
			candidates = discovery.Dedupe(candidates)
			if s.q != "" {
				candidates = discovery.FilterByRelevance(candidates, s.q)
			}
			total := numOf(asMapAny(data["metadata"])["total"])
			servers := sliceRange(candidates, s.offset, s.offset+s.limit)
			// 页内缺安装配置的条目 → 按名回查 npm 补 npx 建议；只补当前页以控制外呼量
			var missing []string
			for _, sv := range servers {
				if sv.Suggestion == nil {
					missing = append(missing, sv.Name)
				}
			}
			if len(missing) > 0 {
				backfilled := npmManifestBackfill(missing)
				for i := range servers {
					if fb, ok := backfilled[servers[i].Name]; ok {
						fbSug := *fb.Suggestion
						fbSug.Name = servers[i].Name // 展示名跟官方条目；npx 参数用 npm 包名
						servers[i].Suggestion = &fbSug
						desc := strings.TrimPrefix(servers[i].Description+" · 官方条目无安装配置，已按同名 npm 包回退", " · ")
						servers[i].Description = desc
					}
				}
			}
			hasMore := false
			if total != nil {
				hasMore = s.offset+len(servers) < int(*total)
			} else {
				hasMore = len(candidates) >= s.offset+s.limit || len(entries) >= fetchLimit
			}
			payload := map[string]any{"servers": servers, "source": source, "hasMore": hasMore}
			if total != nil {
				payload["total"] = int(*total)
				payload["totalPages"] = int(math.Ceil(*total / float64(s.limit)))
			}
			s.sendJSON(200, payload)
		}
		officialSearch := func() { officialLikeSearch(officialBase, "官方 registry", "official", npmSearch) }
		githubSearch := func() { officialLikeSearch(githubRegistry, "GitHub registry", "github", officialSearch) }

		// —— 魔搭 ModelScope MCP 广场（国内源：PUT + JSON body，上游已做相关序） ——
		modelscopeSearch := func() {
			pageSize := clampInt(s.offset+s.limit, 20, 100)
			pageNumber := s.offset/pageSize + 1
			data, err := fetchJSON(goCtx, http.MethodPut, modelscopeAPI, map[string]any{
				"PageSize": pageSize, "PageNumber": pageNumber, "Query": s.q, "Criterion": []any{},
			}, 15*time.Second)
			if err != nil {
				s.fail(fmt.Sprintf("魔搭 ModelScope 不可达: %v", truncateStr(err.Error(), 80)))
				return
			}
			candidates := discovery.Dedupe(discovery.ModelscopeToSuggestions(data))
			from := s.offset - (pageNumber-1)*pageSize
			servers := sliceRange(candidates, from, from+s.limit)
			var total *float64
			if m := asMapAny(asMapAny(data["Data"])["McpServer"]); m != nil {
				total = numOf(m["TotalCount"])
			}
			s.sendJSON(200, paginated(map[string]any{"source": "modelscope"}, servers, total, s.offset, s.limit))
		}

		// —— 百度 MCP World（国内源：GET，pn 从 0 起） ——
		baiduSearch := func() {
			typ := "tag"
			if s.q != "" {
				typ = "search"
			}
			params := url.Values{}
			params.Set("wd", s.q)
			params.Set("type", typ)
			params.Set("pn", fmt.Sprint(s.offset/s.limit))
			params.Set("lg", "zh")
			params.Set("pl", fmt.Sprint(s.limit))
			data, err := fetchJSON(goCtx, http.MethodGet, baiduMcpAPI+"?"+params.Encode(), nil, 15*time.Second)
			if err != nil {
				s.fail(fmt.Sprintf("百度 MCP World 不可达: %v", truncateStr(err.Error(), 80)))
				return
			}
			servers := discovery.Dedupe(discovery.BaiduMcpWorldToSuggestions(data))
			s.sendJSON(200, paginated(map[string]any{"source": "baidu"}, servers, discovery.BaiduMcpWorldTotal(data), s.offset, s.limit))
		}

		// —— 静态目录三源（整包拉取 + 进程内缓存，本地过滤分页） ——
		catalogSearch := func(target, source, label string, mapFn func([]byte) []discovery.Suggestion, attempts int) {
			raw, err := s.fetchCatalogRaw(goCtx, target, attempts)
			if err != nil {
				s.fail(fmt.Sprintf("%s 不可达: %v", label, truncateStr(err.Error(), 80)))
				return
			}
			candidates := mapFn(raw)
			if s.q != "" {
				candidates = discovery.FilterByRelevance(candidates, s.q)
			}
			servers := sliceRange(candidates, s.offset, s.offset+s.limit)
			totalPages := int(math.Ceil(float64(len(candidates)) / float64(s.limit)))
			if totalPages < 1 {
				totalPages = 1
			}
			s.sendJSON(200, map[string]any{
				"servers": servers, "source": source,
				"total": len(candidates), "totalPages": totalPages,
				"hasMore": s.offset+len(servers) < len(candidates),
			})
		}

		if s.q == "" && source == "npm" {
			SendError(ctx.Res, 400, "npm 源需要搜索关键词（npm 没有可浏览的 server 目录）。请输入关键词，或选择其他源。")
			return
		}
		switch source {
		case "smithery":
			smitherySearch()
		case "npm":
			npmSearch()
		case "modelscope":
			modelscopeSearch()
		case "baidu":
			baiduSearch()
		case "cline":
			catalogSearch(clineCatalog, "cline", "Cline 市场", wrapMapMapper(discovery.ClineCatalogToSuggestions), 1)
		case "docker":
			catalogSearch(dockerCatalog, "docker", "Docker MCP 目录", discovery.DockerCatalogToSuggestions, 2)
		case "mcpm":
			catalogSearch(mcpmRegistry, "mcpm", "mcpm 注册表", discovery.McpmRegistryToSuggestions, 1)
		case "github":
			githubSearch()
		default:
			officialSearch()
		}
		// 各源失败时内部已尝试回退；全部失败则在此兜底
		if !s.responded {
			if s.q == "" {
				SendError(ctx.Res, 502, fmt.Sprintf("无法从 %s 源获取列表（%s）。可稍后重试，或切换其他数据源。", source, strings.Join(s.failures, "；")))
			} else {
				SendError(ctx.Res, 502, fmt.Sprintf("所有数据源均不可达（%s）。国际源可能需要网络代理，国内源请检查网络。", strings.Join(s.failures, "；")))
			}
		}
	})
}

// —— 小工具 —— //

func parseIntOr(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

func numOf(v any) *float64 {
	if f, ok := v.(float64); ok {
		return &f
	}
	return nil
}

func asMapAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func asSliceAny(v any) []map[string]any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		if m := asMapAny(x); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func toAnySlice(list []map[string]any) []any {
	out := make([]any, 0, len(list))
	for _, m := range list {
		out = append(out, m)
	}
	return out
}

func sliceRange[T any](list []T, from, to int) []T {
	if from < 0 {
		from = 0
	}
	if from > len(list) {
		from = len(list)
	}
	if to > len(list) {
		to = len(list)
	}
	if to <= from {
		return []T{}
	}
	return list[from:to]
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

var _ = sort.Strings

// wrapMapMapper 把 map 版映射函数适配为 []byte 版。
func wrapMapMapper(fn func(map[string]any) []discovery.Suggestion) func([]byte) []discovery.Suggestion {
	return func(raw []byte) []discovery.Suggestion {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil
		}
		return fn(m)
	}
}
