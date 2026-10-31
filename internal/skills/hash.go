package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SkillAgentDirs 支持 Skill 投影的 agent → skills 目录（相对用户主目录/沙箱根）。
// 均为 SKILL.md 开放标准的落地目录。
var SkillAgentDirs = map[string][]string{
	"claude-code": {".claude", "skills"},
	"codex":       {".codex", "skills"},
	"gemini-cli":  {".gemini", "skills"},
	"opencode":    {".config", "opencode", "skills"},
	"zcode":       {".zcode", "skills"},
	"cursor":      {".cursor", "skills"},
	"windsurf":    {".windsurf", "skills"},
	"trae":        {".trae", "skills"},
}

// SkillAgentList 稳定顺序（投影状态遍历序 = TS Object.keys 序）。
var SkillAgentList = []string{"claude-code", "codex", "gemini-cli", "opencode", "zcode", "cursor", "windsurf", "trae"}

// SkillDirsForAgents 各 agent 的 skills 目录绝对路径（baseDir 缺省为用户主目录）。
func SkillDirsForAgents(baseDir string) map[string]string {
	root := baseDir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		root = home
	}
	out := map[string]string{}
	for agent, segments := range SkillAgentDirs {
		out[agent] = filepath.Join(append([]string{root}, segments...)...)
	}
	return out
}

// ComputeDirHash 目录内容指纹（cc-switch skill.rs:1195-1215 同款）：
// 递归收集非隐藏文件，按相对路径（`\`→`/`）排序，对 "rel\0" + 内容 + "\0" 逐文件喂同一 SHA-256。
// 目录存在性由调用方保证（更新检查必须先确认目录存在再算，否则换机恢复缺失会被误报"无更新"）。
func ComputeDirHash(dir string) (string, error) {
	files, err := listFiles(dir, dir)
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(a, b int) bool { return files[a].rel < files[b].rel })
	hash := sha256.New()
	for _, f := range files {
		content, err := withRetry(func() ([]byte, error) { return os.ReadFile(f.abs) })
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, f.rel)
		hash.Write([]byte{0})
		hash.Write(content)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type fileRef struct {
	rel string
	abs string
}

func listFiles(root, dir string) ([]fileRef, error) {
	out := []fileRef{}
	entries, err := withRetry(func() ([]os.DirEntry, error) { return os.ReadDir(dir) })
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // 跳过隐藏文件/目录
		}
		abs := filepath.Join(dir, e.Name())
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if e.IsDir() {
			sub, err := listFiles(root, abs)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		} else if e.Type().IsRegular() {
			out = append(out, fileRef{rel: rel, abs: abs})
		}
		// 其他类型（socket 等）不参与指纹
	}
	return out, nil
}

// isTransientLockErr Windows 瞬时文件锁：杀软/索引器会短暂占用刚写入的文件。
func isTransientLockErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "being used by another process") ||
		strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "Access is denied")
}

// withRetry 瞬时锁指数退避重试（10ms 起，6 次）。
func withRetry[T any](fn func() (T, error)) (T, error) {
	var lastErr error
	for i := 0; i < 6; i++ {
		v, err := fn()
		if err == nil {
			return v, nil
		}
		lastErr = err
		if !isTransientLockErr(err) {
			var zero T
			return zero, err
		}
		time.Sleep(10 * time.Millisecond << uint(i))
	}
	var zero T
	return zero, lastErr
}
