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

// skillsShBaseURL 测试可覆盖（真实地址 skills.sh 搜索 API）。
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
// （cc-switch skill.rs:4425-4486 的同名过滤）。
func SearchSkillsSh(ctx context.Context, q string, limit, offset int, timeout time.Duration) (SearchResult, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	target := fmt.Sprintf("%s?q=%s&limit=%d&offset=%d", skillsShBaseURL, url.QueryEscape(q), limit, offset)
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
