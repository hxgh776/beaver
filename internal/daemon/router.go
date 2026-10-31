package daemon

import (
	"net/http"
	"net/url"
	"strings"
)

// RequestContext 路由处理上下文：参数、查询与懒解析的 JSON body。
type RequestContext struct {
	Req    *http.Request
	Res    http.ResponseWriter
	Params map[string]string
	Query  url.Values

	body      map[string]any
	bodyRead  bool
	bodyError error
}

// Body 懒解析 JSON body（多次调用复用缓存）。
func (c *RequestContext) Body() (map[string]any, error) {
	if !c.bodyRead {
		c.body, c.bodyError = ReadJSONBody(c.Req.Body)
		c.bodyRead = true
	}
	return c.body, c.bodyError
}

type route struct {
	method   string
	segments []string
	handler  func(*RequestContext)
}

// Router 极简路由：'/api/servers/:id' 风格模式匹配，零依赖（§1.2 选型）。
type Router struct {
	routes []route
}

func splitSegments(s string) []string {
	raw := strings.Split(strings.Trim(s, "/"), "/")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (rt *Router) Add(method, pattern string, handler func(*RequestContext)) *Router {
	rt.routes = append(rt.routes, route{
		method:   strings.ToUpper(method),
		segments: splitSegments(pattern),
		handler:  handler,
	})
	return rt
}

func (rt *Router) Get(pattern string, handler func(*RequestContext)) *Router {
	return rt.Add(http.MethodGet, pattern, handler)
}

func (rt *Router) Post(pattern string, handler func(*RequestContext)) *Router {
	return rt.Add(http.MethodPost, pattern, handler)
}

func (rt *Router) Put(pattern string, handler func(*RequestContext)) *Router {
	return rt.Add(http.MethodPut, pattern, handler)
}

func (rt *Router) Patch(pattern string, handler func(*RequestContext)) *Router {
	return rt.Add(http.MethodPatch, pattern, handler)
}

func (rt *Router) Delete(pattern string, handler func(*RequestContext)) *Router {
	return rt.Add(http.MethodDelete, pattern, handler)
}

// Any 接受任意方法（用于占位路由）。
func (rt *Router) Any(pattern string, handler func(*RequestContext)) *Router {
	rt.routes = append(rt.routes, route{
		method:   "*",
		segments: splitSegments(pattern),
		handler:  handler,
	})
	return rt
}

// Dispatch 返回 true 表示已处理（含 404 兜底由调用方负责）。
func (rt *Router) Dispatch(req *http.Request, res http.ResponseWriter, pathname string, query url.Values) bool {
	filtered := splitSegments(pathname)
	for i := range rt.routes {
		r := &rt.routes[i]
		if r.method != "*" && r.method != req.Method {
			continue
		}
		if len(r.segments) != len(filtered) {
			continue
		}
		params := map[string]string{}
		matched := true
		for j, seg := range r.segments {
			part := filtered[j]
			if strings.HasPrefix(seg, ":") {
				if p, err := url.PathUnescape(part); err == nil {
					params[seg[1:]] = p
				} else {
					params[seg[1:]] = part
				}
			} else if seg != part {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		ctx := &RequestContext{Req: req, Res: res, Params: params, Query: query}
		r.handler(ctx)
		return true
	}
	return false
}
