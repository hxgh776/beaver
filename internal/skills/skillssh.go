package skills

// skills.sh 搜索客户端与 GitHub 坐标校验（对应 TS packages/core/src/skills/skills-sh.ts）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// skillsShBaseURL 生产搜索 API 地址（base 参数为空时的缺省值；测试经 ServiceOptions.SkillsShBase 注入桩）。
var skillsShBaseURL = "https://skills.sh/api/search"

// GitHub 坐标白名单：owner 不含点（拒绝 skills.volces.com 这类伪 owner），全段拒绝路径穿越
var (
	ownerRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// SkillInputError 用户输入不合法。
type SkillInputError struct{ msg string }

func (e *SkillInputError) Error() string { return e.msg }

func inputErrf(format string, args ...any) error {
	return &SkillInputError{msg: fmt.Sprintf(format, args...)}
}

// RepoRef 合法 GitHub 坐标。
type RepoRef struct {
	Owner  string
	Name   string
	Branch string
}

// ValidateRepoRef 仓库坐标 + 分支白名单校验。
func ValidateRepoRef(repo, branch string) (RepoRef, error) {
	slash := strings.Index(repo, "/")
	if slash <= 0 || slash == len(repo)-1 {
		return RepoRef{}, inputErrf("非法仓库坐标: %s", repo)
	}
	owner, name := repo[:slash], repo[slash+1:]
	if !ownerRe.MatchString(owner) || !repoRe.MatchString(name) || strings.Contains(name, "..") {
		return RepoRef{}, inputErrf("非法仓库坐标: %s", repo)
	}
	if !branchRe.MatchString(branch) || strings.Contains(branch, "..") {
		return RepoRef{}, inputErrf("非法分支名: %s", branch)
	}
	return RepoRef{Owner: owner, Name: name, Branch: branch}, nil
}

// NormalizeRepoAddress 归一化 GitHub 仓库地址，支持三种形式：
//   - owner/repo
//   - https://github.com/owner/repo（`.git` 后缀、尾斜杠可选）
//   - https://github.com/owner/repo/tree/<branch>（更深的子路径只取分支）
//
// 返回的 Branch 为空表示地址未指定分支，交由安装流程回退探测 main/master。
// 非 GitHub 地址、非法坐标与路径穿越一律返回 SkillInputError。
func NormalizeRepoAddress(raw string) (RepoRef, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return RepoRef{}, inputErrf("仓库地址不能为空")
	}
	if strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "github.com/") {
		return normalizeGithubURL(s)
	}
	// 纯坐标 owner/repo：仅允许一个斜杠，多余路径段明确拒绝
	if strings.Count(s, "/") != 1 {
		return RepoRef{}, inputErrf("非法仓库地址: %s", raw)
	}
	ref, err := ValidateRepoRef(s, "main")
	if err != nil {
		return RepoRef{}, err
	}
	ref.Branch = ""
	return ref, nil
}

// normalizeGithubURL 解析 GitHub URL，提取 owner/repo 与可选分支。
func normalizeGithubURL(raw string) (RepoRef, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return RepoRef{}, inputErrf("非法仓库地址: %s", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return RepoRef{}, inputErrf("仅支持 GitHub 仓库地址: %s", raw)
	}
	segs := []string{}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	if len(segs) < 2 {
		return RepoRef{}, inputErrf("GitHub 地址缺少 owner/repo: %s", raw)
	}
	owner, name := segs[0], strings.TrimSuffix(segs[1], ".git")
	branch := ""
	switch {
	case len(segs) == 2:
		// 仓库根
	case len(segs) >= 3 && segs[2] == "tree":
		if len(segs) < 4 {
			return RepoRef{}, inputErrf("仓库地址缺少分支: %s", raw)
		}
		branch = segs[3] // tree/<branch>/<子路径> 只取分支，忽略更深的子路径
	default:
		return RepoRef{}, inputErrf("仅支持仓库根或 /tree/<branch> 地址: %s", raw)
	}
	ref, err := ValidateRepoRef(owner+"/"+name, firstNonEmpty(branch, "main"))
	if err != nil {
		return RepoRef{}, err
	}
	ref.Branch = branch
	return ref, nil
}

// GithubArchiveURL GitHub 归档 URL 出口断言：拼接结果必须仍是本仓库的 archive 路径（防 branch 劫持落点）。
func GithubArchiveURL(owner, name, branch string) (string, error) {
	for _, seg := range strings.Split(branch, "/") {
		if seg == ".." || seg == "." {
			return "", inputErrf("非法分支名: %s", branch)
		}
	}
	target := fmt.Sprintf("https://github.com/%s/%s/archive/refs/heads/%s.zip", owner, name, branch)
	expected := fmt.Sprintf("https://github.com/%s/%s/archive/", owner, name)
	if !strings.HasPrefix(target, expected) {
		return "", inputErrf("归档 URL 校验失败: %s", target)
	}
	return target, nil
}

// SearchRow skills.sh 搜索结果行。
type SearchRow struct {
	SkillID  string  `json:"skillId"`
	Name     string  `json:"name"`
	Installs float64 `json:"installs"`
	// Repo GitHub 坐标 owner/repo（已过白名单校验）
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

// SearchResult skills.sh 搜索结果。
type SearchResult struct {
	Query  string      `json:"query"`
	Count  float64     `json:"count"`
	Skills []SearchRow `json:"skills"`
}

// SearchSkillsSh skills.sh 搜索；source 必须能拆成合法 GitHub 坐标，否则丢弃该行
// （cc-switch skill.rs:4425-4486 的同名过滤）。base 为空时用生产地址（测试可注入桩 URL）。
func SearchSkillsSh(ctx context.Context, base, q string, limit, offset int, timeout time.Duration) (SearchResult, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if base == "" {
		base = skillsShBaseURL
	}
	target := fmt.Sprintf("%s?q=%s&limit=%d&offset=%d", base, url.QueryEscape(q), limit, offset)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return SearchResult{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return SearchResult{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return SearchResult{}, fmt.Errorf("skills.sh 搜索失败: HTTP %d", res.StatusCode)
	}
	var raw struct {
		Count  *float64 `json:"count"`
		Skills []struct {
			SkillID  *string  `json:"skillId"`
			Name     *string  `json:"name"`
			Installs *float64 `json:"installs"`
			Source   *string  `json:"source"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return SearchResult{}, err
	}
	skills := []SearchRow{}
	seen := map[string]struct{}{}
	for _, s := range raw.Skills {
		if s.Source == nil || s.Name == nil {
			continue
		}
		source := *s.Source
		if !strings.Contains(source, "/") {
			continue // owner/repo 坐标
		}
		branch := "main"
		repo := source
		// 形如 owner/repo@branch 的坐标取分支；纯 owner/repo 一律假定 main
		if strings.Contains(repo, "@") {
			parts := strings.SplitN(repo, "@", 2)
			if parts[0] == "" || parts[1] == "" {
				continue
			}
			repo, branch = parts[0], parts[1]
		}
		if _, err := ValidateRepoRef(repo, branch); err != nil {
			continue // 非 GitHub 坐标直接过滤
		}
		skillID := *s.Name
		if s.SkillID != nil {
			skillID = *s.SkillID
		}
		key := repo + "#" + skillID
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		installs := 0.0
		if s.Installs != nil {
			installs = *s.Installs
		}
		skills = append(skills, SearchRow{SkillID: skillID, Name: *s.Name, Installs: installs, Repo: repo, Branch: branch})
	}
	count := float64(len(skills))
	if raw.Count != nil {
		count = *raw.Count
	}
	return SearchResult{Query: q, Count: count, Skills: skills}, nil
}
