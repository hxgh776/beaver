package skills

// ZIP 下载与安全解压（对应 TS packages/core/src/skills/zip.ts；
// 供应链安全预算照抄 cc-switch skill.rs:386-402, 3598-3716）。

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ZIP 供应链安全预算
const (
	MaxDownloadBytes = int64(128) * 1024 * 1024 // 128MiB，逐块计数，不信 Content-Length
	MaxExtractBytes  = int64(512) * 1024 * 1024 // 512MiB 解压预算（压缩炸弹）
	MaxEntries       = 30000
	DownloadTimeout  = 60 * time.Second
)

// SkillZipError ZIP 下载/解压错误。
type SkillZipError struct{ msg string }

func (e *SkillZipError) Error() string { return e.msg }

func zipErrf(format string, args ...any) error {
	return &SkillZipError{msg: fmt.Sprintf(format, args...)}
}

// DownloadZip 下载 ZIP 到内存。逐块计数 128MiB 上限；网络失败时依次尝试镜像前缀
// （github 直连不稳定，ghfast.top 为已知可用镜像；HTTP 状态错误不换镜像——镜像不会让 404 变 200）。
func DownloadZip(ctx context.Context, url string) ([]byte, error) {
	prefixes := []string{"", mirrorPrefix()}
	var lastErr error
	for _, prefix := range prefixes {
		data, err := downloadOnce(ctx, prefix+url)
		if err == nil {
			return data, nil
		}
		lastErr = err
		var zerr *SkillZipError
		if errors.As(err, &zerr) && strings.Contains(zerr.msg, "HTTP ") {
			return nil, err
		}
	}
	if lastErr == nil {
		lastErr = zipErrf("下载失败")
	}
	return nil, lastErr
}

func mirrorPrefix() string {
	if v := os.Getenv("AGENT_CONTROL_GITHUB_MIRROR"); v != "" {
		return v
	}
	return "https://ghfast.top/"
}

func downloadOnce(ctx context.Context, url string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, zipErrf("下载失败: HTTP %d %s", res.StatusCode, url)
	}
	var buf bytes.Buffer
	// 逐块计数上限（不信任 Content-Length）
	if _, err := io.Copy(&buf, io.LimitReader(res.Body, MaxDownloadBytes+1)); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > MaxDownloadBytes {
		return nil, zipErrf("下载体积超过 %dMiB 上限", MaxDownloadBytes/1024/1024)
	}
	return buf.Bytes(), nil
}

// ExtractZip 安全解压到 destRoot（必须已存在且为空）。
// 双重 zip-slip 防线：剥掉归档根目录组件后再逐组件校验，最终路径必须仍在 destRoot 内；
// symlink 条目拒收（内存炸弹向量）；条目数/解压字节数双预算。
func ExtractZip(data []byte, destRoot string) error {
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return zipErrf("ZIP 解析失败: %v", err)
	}
	if len(reader.File) == 0 {
		return zipErrf("ZIP 为空")
	}
	if len(reader.File) > MaxEntries {
		return zipErrf("条目数超过 %d 上限", MaxEntries)
	}
	rootResolved, err := filepath.Abs(destRoot)
	if err != nil {
		return err
	}
	// 剥根：所有条目共享同一第一层组件（GitHub 归档恒为 repo-branch/）才剥，无根 zip 原样解压
	firstComponents := map[string]struct{}{}
	for _, f := range reader.File {
		parts := splitEntryPath(f.Name)
		key := ""
		if len(parts) > 0 {
			key = parts[0]
		}
		firstComponents[key] = struct{}{}
	}
	stripRoot := ""
	if len(firstComponents) == 1 {
		for k := range firstComponents {
			if k != "" {
				stripRoot = k
			}
		}
	}
	entryCount := 0
	totalBytes := int64(0)
	for _, f := range reader.File {
		entryCount++ // 空文件/目录也按一块计费
		if entryCount > MaxEntries {
			return zipErrf("条目数超过 %d 上限", MaxEntries)
		}
		// 与 TS/yauzl 同判法：无条件读外部属性高 16 位的 unix mode（Go 的 f.Mode() 会因
		// CreatorVersion 非 unix 而忽略它，漏放 symlink 条目）
		if (f.ExternalAttrs>>16)&0xf000 == 0xa000 {
			return zipErrf("ZIP 含 symlink 条目，拒收: %s", f.Name)
		}
		target, ok := SafeExtractPath(rootResolved, f.Name, stripRoot)
		if !ok {
			return zipErrf("ZIP 条目路径越界，拒收: %s", f.Name)
		}
		if strings.HasSuffix(filepath.ToSlash(f.Name), "/") || target == rootResolved {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		totalBytes += int64(f.UncompressedSize64)
		if totalBytes > MaxExtractBytes {
			return zipErrf("解压体积超过 %dMiB 上限", MaxExtractBytes/1024/1024)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		// 声明尺寸可能是谎言：实际解压字节同样受总预算约束
		if err := extractEntry(f, target, int64(f.UncompressedSize64), totalBytes); err != nil {
			return err
		}
	}
	return nil
}

func extractEntry(f *zip.File, target string, declaredSize, budgetBase int64) error {
	src, err := f.Open()
	if err != nil {
		return zipErrf("条目读取失败: %s", f.Name)
	}
	defer src.Close()
	dst, err := os.Create(target)
	if err != nil {
		return zipErrf("条目写入失败: %s: %v", f.Name, err)
	}
	defer dst.Close()
	written := int64(0)
	buf := make([]byte, 64*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > declaredSize || budgetBase+written-declaredSize > MaxExtractBytes {
				return zipErrf("条目实际解压尺寸超出预算: %s", f.Name)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return zipErrf("条目写入失败: %s: %v", f.Name, werr)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return zipErrf("条目解压失败: %v", rerr)
		}
	}
	return nil
}

func splitEntryPath(name string) []string {
	normalized := strings.ReplaceAll(name, "\\", "/")
	parts := strings.Split(normalized, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SafeExtractPath 归档内路径 → 目标磁盘路径；非法返回 false。
// 先做 enclosed_name 语义校验：任何位置出现 `..`/盘符/NUL 整体拒收——
// 剥归档根目录这一步不得"吃掉"前导 `..`（skill.rs:3756-3780 双防线），
// 最后做 resolved 前缀断言兜底。stripRoot 为调用方探测出的公共根组件（可选）。
func SafeExtractPath(destRoot, entryName, stripRoot string) (string, bool) {
	if strings.ContainsRune(entryName, '\x00') {
		return "", false
	}
	parts := splitEntryPath(entryName)
	if len(parts) == 0 {
		return "", false
	}
	for _, p := range parts {
		if p == ".." || isDriveLetter(p) {
			return "", false
		}
	}
	rel := parts
	if stripRoot != "" && rel[0] == stripRoot {
		rel = rel[1:]
	}
	if len(rel) == 0 {
		return destRoot, true // 归档根目录条目本身 → 目标根
	}
	resolved := filepath.Join(append([]string{destRoot}, rel...)...)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", false
	}
	if abs != destRoot && !strings.HasPrefix(abs, destRoot+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

func isDriveLetter(p string) bool {
	return len(p) == 2 && p[1] == ':' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}
