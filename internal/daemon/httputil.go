package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxBodyBytes = 1024 * 1024

// SendJSON 以统一 JSON 响应头写状态码与数据。
func SendJSON(w http.ResponseWriter, status int, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		body = []byte(`{"error":"响应序列化失败"}`)
		status = 500
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// SendError 写 {"error": message}（code 非空时附带）。
func SendError(w http.ResponseWriter, status int, message string, code ...string) {
	payload := map[string]any{"error": message}
	if len(code) > 0 && code[0] != "" {
		payload["code"] = code[0]
	}
	SendJSON(w, status, payload)
}

// ReadJSONBody 读取请求体为 JSON；空 body 视为 {}（TS readJsonBody 同语义）。
func ReadJSONBody(r io.Reader) (map[string]any, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	if len(data) > maxBodyBytes {
		return nil, fmt.Errorf("body too large")
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("请求体不是合法 JSON 对象: %w", err)
	}
	return out, nil
}

// CheckToken Bearer token 校验（G9）：支持 Authorization 头或 ?token= 查询参数。
func CheckToken(r *http.Request, expectedToken string, query url.Values) bool {
	if r.Header.Get("Authorization") == "Bearer "+expectedToken {
		return true
	}
	return query.Get("token") == expectedToken && expectedToken != ""
}
