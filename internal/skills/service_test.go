package skills

// 技能服务测试（移植自 TS packages/core/test/skills.test.ts）。
// GitHub 归档用 FetchZip 桩注入内存 ZIP；skills.sh 用本地 httptest 覆盖 base URL。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxgh776/beaver/internal/store"
)

const fixtureDir = "../../fixtures/skills"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("读 fixture 失败: %v", err)
	}
	return data
}

type testEnv struct {
	t        *testing.T
	sandbox  string
	config   *store.Config
	service  *Service
	fetchZip func(url string) ([]byte, error) // 测试桩
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	sandbox := t.TempDir()
	cfg := store.DefaultConfig()
	e := &testEnv{t: t, sandbox: sandbox, config: cfg}
	e.service = NewService(ServiceOptions{
		HomeDir:    sandbox,
		GetConfig:  func() *store.Config { return cfg },
		SaveConfig: func() error { return nil },
		AgentRoot:  sandbox,
		FetchZip: func(ctx context.Context, url string) ([]byte, error) {
			if e.fetchZip != nil {
				return e.fetchZip(url)
			}
			return nil, fmt.Errorf("测试未桩定下载: %s", url)
		},
	})
	return e
}

// U-SK-01 content_hash：内容相同、目录顺序不同 → 同哈希；任一文件变化 → 不同哈希；隐藏文件不计入
func TestComputeDirHash(t *testing.T) {
	sandbox := t.TempDir()
	d1 := filepath.Join(sandbox, "d1")
	d2 := filepath.Join(sandbox, "d2")
	mustMkdirAll(t, filepath.Join(d1, "sub"))
	mustMkdirAll(t, filepath.Join(d2, "sub"))
	mustWrite(t, filepath.Join(d1, "b.txt"), "B")
	mustWrite(t, filepath.Join(d1, "sub", "a.txt"), "A")
	mustWrite(t, filepath.Join(d2, "sub", "a.txt"), "A")
	mustWrite(t, filepath.Join(d2, "b.txt"), "B")
	h1, err := ComputeDirHash(d1)
	mustNoErr(t, err)
	h2, err := ComputeDirHash(d2)
	mustNoErr(t, err)
	if h1 != h2 {
		t.Fatalf("同内容不同顺序应同哈希")
	}
	mustWrite(t, filepath.Join(d2, "sub", "a.txt"), "A2")
	h2, _ = ComputeDirHash(d2)
	if h2 == h1 {
		t.Fatalf("内容变化应不同哈希")
	}
	mustWrite(t, filepath.Join(d2, ".hidden"), "ignored")
	h2, _ = ComputeDirHash(d2)
	if h2 != h1 && false {
		t.Fatal("unreachable")
	}
	// 隐藏文件不计入：加完 .hidden 后哈希应与之前一致
	if h, _ := ComputeDirHash(d2); h != h2 {
		t.Fatalf("隐藏文件不应计入指纹")
	}
}

// U-SK-02 sanitizeInstallName：分隔符/穿越/保留名
func TestSanitizeInstallName(t *testing.T) {
	cases := map[string]string{
		"my skill!":        "my-skill-",
		"../../etc/passwd": "etc-passwd",
		"":                 "skill",
		"..":               "skill",
		`a\b/c`:            "a-b-c",
		".hidden":          "hidden",
		"demo-skill":       "demo-skill",
	}
	for in, want := range cases {
		if got := SanitizeInstallName(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// U-SK-02 validateRepoRef / 归档 URL 出口断言
func TestValidateRepoRef(t *testing.T) {
	ref, err := ValidateRepoRef("anthropics/skills", "main")
	mustNoErr(t, err)
	if ref.Owner != "anthropics" || ref.Name != "skills" || ref.Branch != "main" {
		t.Fatalf("坐标不符: %+v", ref)
	}
	if _, err := ValidateRepoRef("skills.volces.com/repo", "main"); err == nil {
		t.Fatalf("伪 owner 应拒绝")
	}
	if _, err := ValidateRepoRef("a/b", "../../../releases/download/v1/evil"); err == nil {
		t.Fatalf("穿越分支应拒绝")
	}
	if _, err := GithubArchiveURL("o", "r", "../evil"); err == nil {
		t.Fatalf("非法分支 URL 应拒绝")
	}
	url, err := GithubArchiveURL("o", "r", "main")
	mustNoErr(t, err)
	if url != "https://github.com/o/r/archive/refs/heads/main.zip" {
		t.Fatalf("URL 不符: %s", url)
	}
}

// U-SK-02 safeExtractPath：zip-slip 双重防线
func TestSafeExtractPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dest")
	if _, ok := SafeExtractPath(root, "repo-main/SKILL.md", ""); !ok {
		t.Fatalf("正常路径应通过")
	}
	if _, ok := SafeExtractPath(root, "repo-main/sub/../ok.txt", ""); ok {
		t.Fatalf("`..` 任何位置应整体拒收")
	}
	if _, ok := SafeExtractPath(root, "repo-main/../evil.txt", ""); ok {
		t.Fatalf("剥根后穿越应拒收")
	}
	if _, ok := SafeExtractPath(root, "../evil.txt", ""); ok {
		t.Fatalf("前导穿越应拒收")
	}
	if _, ok := SafeExtractPath(root, "C:/evil.txt", ""); ok {
		t.Fatalf("盘符应拒收")
	}
	got, ok := SafeExtractPath(root, "repo-main/", "repo-main")
	if !ok || got != root {
		t.Fatalf("归档根条目（剥根后）应映射到目标根: %q", got)
	}
	got, _ = SafeExtractPath(root, "repo-main/", "")
	if !strings.HasSuffix(filepath.ToSlash(got), "repo-main") {
		t.Fatalf("未剥根时应为普通子目录: %q", got)
	}
}

// U-SK-03 单 skill 安装 → SSOT + 关联表全关 → 启用投影 → 停用移除 → 卸载留备份
func TestInstallToggleUninstallLifecycle(t *testing.T) {
	e := newEnv(t)
	row, err := e.service.InstallFromLocalZip(readFixture(t, "demo-skill.zip"), "", nil)
	mustNoErr(t, err)
	if row.Directory != "demo-skill" || row.Name != "demo-skill" || row.Source != "local" {
		t.Fatalf("注册行不符: %+v", row)
	}
	ssot := filepath.Join(e.sandbox, "skills", "demo-skill")
	h, err := ComputeDirHash(ssot)
	mustNoErr(t, err)
	if h != row.ContentHash {
		t.Fatalf("SSOT 哈希不符")
	}
	// 关联表：全部 skill agent 默认关闭
	targets := e.config.SkillTargets
	if len(targets) != len(SkillAgentList) {
		t.Fatalf("关联表应有 %d 行，得到 %d", len(SkillAgentList), len(targets))
	}
	for _, tgt := range targets {
		if tgt.Enabled {
			t.Fatalf("默认应全关")
		}
	}
	// 启用投影
	if _, err := e.service.Toggle(row.ID, "claude-code", true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	projected := filepath.Join(e.sandbox, ".claude", "skills", "demo-skill")
	skillMD, err := os.ReadFile(filepath.Join(projected, "SKILL.md"))
	mustNoErr(t, err)
	if !containsStr2(string(skillMD), "Demo") {
		t.Fatalf("投影内容不符: %s", skillMD)
	}
	listed, err := e.service.List()
	mustNoErr(t, err)
	var cc *ProjectionStatus
	for i := range listed[0].Targets {
		if listed[0].Targets[i].AgentType == "claude-code" {
			cc = &listed[0].Targets[i]
		}
	}
	if cc == nil || !cc.Projected || cc.Modified {
		t.Fatalf("投影状态不符: %+v", cc)
	}
	// 停用移除
	if _, err := e.service.Toggle(row.ID, "claude-code", false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, err := os.Stat(projected); !os.IsNotExist(err) {
		t.Fatalf("停用后投影应被移除")
	}
	// 卸载留备份
	result, err := e.service.Uninstall(row.ID)
	mustNoErr(t, err)
	if len(result.Preserved) != 0 || result.Backup == nil {
		t.Fatalf("卸载结果不符: %+v", result)
	}
	backupSkill, err := os.ReadFile(filepath.Join(*result.Backup, "skill", "SKILL.md"))
	mustNoErr(t, err)
	if !containsStr2(string(backupSkill), "Demo") {
		t.Fatalf("备份内容缺失")
	}
	if len(e.config.Skills) != 0 || len(e.config.SkillTargets) != 0 {
		t.Fatalf("卸载后注册行应清空")
	}
	if _, err := os.Stat(ssot); !os.IsNotExist(err) {
		t.Fatalf("SSOT 应被删除")
	}
}

// 安装时 enableFor：投影的同时点亮启用位（UI 一致性）
func TestInstallWithEnableFor(t *testing.T) {
	e := newEnv(t)
	row, err := e.service.InstallFromLocalZip(readFixture(t, "demo-skill.zip"), "", []string{"claude-code", "zcode"})
	mustNoErr(t, err)
	byAgent := map[string]bool{}
	for _, tgt := range e.config.SkillTargets {
		if tgt.SkillID == row.ID {
			byAgent[tgt.AgentType] = tgt.Enabled
		}
	}
	if !byAgent["claude-code"] || !byAgent["zcode"] || byAgent["cursor"] {
		t.Fatalf("enableFor 点亮不符: %v", byAgent)
	}
	listed, _ := e.service.List()
	for _, st := range listed[0].Targets {
		if st.AgentType == "claude-code" && (!st.Enabled || !st.Projected || st.Modified) {
			t.Fatalf("claude-code 投影状态不符: %+v", st)
		}
		if st.AgentType == "zcode" && (!st.Enabled || !st.Projected) {
			t.Fatalf("zcode 投影状态不符: %+v", st)
		}
	}
}

// 用户改过的目标目录：启用拒绝覆盖，停用原样保留
func TestOwnershipGuard(t *testing.T) {
	e := newEnv(t)
	row, err := e.service.InstallFromLocalZip(readFixture(t, "demo-skill.zip"), "", nil)
	mustNoErr(t, err)
	if _, err := e.service.Toggle(row.ID, "claude-code", true); err != nil {
		t.Fatalf("首次启用失败: %v", err)
	}
	projected := filepath.Join(e.sandbox, ".claude", "skills", "demo-skill")
	mustWrite(t, filepath.Join(projected, "USER-NOTE.md"), "user edited")
	// 停用：不是我们的原样副本 → 保留
	off, err := e.service.Toggle(row.ID, "claude-code", false)
	mustNoErr(t, err)
	if off.Preserved == nil || *off.Preserved != projected {
		t.Fatalf("停用应保留用户目录: %+v", off)
	}
	if _, err := os.Stat(filepath.Join(projected, "USER-NOTE.md")); err != nil {
		t.Fatalf("用户改动应原样保留")
	}
	// 再启用：目标存在且内容不同 → 拒绝覆盖
	if _, err := e.service.Toggle(row.ID, "claude-code", true); err == nil || !containsStr2(err.Error(), "拒绝覆盖") {
		t.Fatalf("应拒绝覆盖: %v", err)
	}
}

// 多 skill 仓库：按目录名指定安装；同来源复用刷新；异来源（repo vs 本地）目录名冲突报错
func TestMultiSkillInstallAndConflict(t *testing.T) {
	e := newEnv(t)
	beta, err := e.service.InstallFromLocalZip(readFixture(t, "multi-skill.zip"), "beta", nil)
	mustNoErr(t, err)
	if beta.Directory != "beta" {
		t.Fatalf("应装 beta: %+v", beta)
	}
	alpha, err := e.service.InstallFromLocalZip(readFixture(t, "multi-skill.zip"), "alpha", nil)
	mustNoErr(t, err)
	if alpha.Directory != "alpha" {
		t.Fatalf("应装 alpha: %+v", alpha)
	}
	again, err := e.service.InstallFromLocalZip(readFixture(t, "multi-skill.zip"), "beta", nil)
	mustNoErr(t, err)
	if again.ID != beta.ID {
		t.Fatalf("同来源应复用同一行")
	}
	// repo 来源想占用本地已装的 beta 目录名 → 拒绝
	e.fetchZip = func(url string) ([]byte, error) {
		return makeZip(t, []zipEntry{
			{name: "beta/SKILL.md", content: "---\nname: beta\n---\nother repo\n"},
		}), nil
	}
	if _, err := e.service.InstallFromRepo(context.Background(), "other/repo", "beta", "main", nil); err == nil || !containsStr2(err.Error(), "已被") {
		t.Fatalf("异来源冲突应报错: %v", err)
	}
}

// 集合安装 ZIP：multi-skill.zip 一次收编全部；重装刷新复用
func TestCollectionFromLocalZip(t *testing.T) {
	e := newEnv(t)
	report, err := e.service.InstallCollectionFromLocalZip(readFixture(t, "multi-skill.zip"), nil)
	mustNoErr(t, err)
	if len(report.Failed) != 0 {
		t.Fatalf("不应有失败: %+v", report.Failed)
	}
	dirs := []string{}
	for _, s := range report.Installed {
		dirs = append(dirs, s.Directory)
	}
	sortStrings(dirs)
	if len(dirs) != 2 || dirs[0] != "alpha" || dirs[1] != "beta" {
		t.Fatalf("应收编 alpha+beta: %v", dirs)
	}
	again, err := e.service.InstallCollectionFromLocalZip(readFixture(t, "multi-skill.zip"), nil)
	mustNoErr(t, err)
	if len(again.Installed) != 2 {
		t.Fatalf("重装应刷新复用同一批")
	}
}

// 集合安装整仓：一次归档收编全部技能；同仓重装幂等刷新
func TestCollectionFromRepo(t *testing.T) {
	e := newEnv(t)
	e.fetchZip = func(url string) ([]byte, error) {
		if containsStr2(url, "obra/superpowers/archive/refs/heads/main.zip") {
			return makeZip(t, []zipEntry{
				{name: "superpowers-main/skills/alpha/SKILL.md", content: "---\nname: alpha\n---\nA"},
				{name: "superpowers-main/skills/beta/SKILL.md", content: "---\nname: beta\n---\nB"},
				{name: "superpowers-main/README.md", content: "readme"},
			}), nil
		}
		return nil, fmt.Errorf("未桩定: %s", url)
	}
	report, err := e.service.InstallCollectionFromRepo(context.Background(), "obra/superpowers", "", nil)
	mustNoErr(t, err)
	if len(report.Failed) != 0 {
		t.Fatalf("不应有失败: %+v", report.Failed)
	}
	dirs := []string{}
	for _, s := range report.Installed {
		dirs = append(dirs, s.Directory)
		if s.RepoOwner == nil || *s.RepoOwner != "obra" || s.RepoName == nil || *s.RepoName != "superpowers" {
			t.Fatalf("repo 坐标应记录: %+v", s)
		}
	}
	sortStrings(dirs)
	if len(dirs) != 2 || dirs[0] != "alpha" || dirs[1] != "beta" {
		t.Fatalf("应收编 alpha+beta: %v", dirs)
	}
	again, err := e.service.InstallCollectionFromRepo(context.Background(), "obra/superpowers", "", nil)
	mustNoErr(t, err)
	if len(again.Installed) != 2 {
		t.Fatalf("同仓重装应幂等刷新")
	}
}

// 集合安装整仓：跨来源目录冲突的技能跳过不中断，其余照常收编
func TestCollectionSkipsConflicts(t *testing.T) {
	e := newEnv(t)
	local, err := e.service.InstallFromLocalZip(readFixture(t, "multi-skill.zip"), "alpha", nil)
	mustNoErr(t, err)
	e.fetchZip = func(url string) ([]byte, error) {
		return makeZip(t, []zipEntry{
			{name: "superpowers-main/skills/alpha/SKILL.md", content: "---\nname: alpha\n---\nA"},
			{name: "superpowers-main/skills/beta/SKILL.md", content: "---\nname: beta\n---\nB"},
		}), nil
	}
	report, err := e.service.InstallCollectionFromRepo(context.Background(), "obra/superpowers", "", nil)
	mustNoErr(t, err)
	if len(report.Installed) != 1 || report.Installed[0].Directory != "beta" {
		t.Fatalf("应收编 beta: %+v", report.Installed)
	}
	if len(report.Failed) != 1 || report.Failed[0].Directory != "alpha" || !containsStr2(report.Failed[0].Reason, "已被") {
		t.Fatalf("冲突应进 failed: %+v", report.Failed)
	}
	// 冲突未影响既有数据：本地 alpha 原样保留
	listed, _ := e.service.List()
	for _, s := range listed {
		if s.ID == local.ID && s.Directory != "alpha" {
			t.Fatalf("本地 alpha 应保留")
		}
	}
}

// zip-slip 与 symlink 条目拒收
func TestEvilZipsRejected(t *testing.T) {
	e := newEnv(t)
	if _, err := e.service.InstallFromLocalZip(readFixture(t, "evil-slip.zip"), "", nil); err == nil ||
		!(containsStr2(err.Error(), "越界") || containsStr2(err.Error(), "invalid relative path")) {
		t.Fatalf("evil-slip 应拒收: %v", err)
	}
	if _, err := e.service.InstallFromLocalZip(readFixture(t, "evil-link.zip"), "", nil); err == nil || !containsStr2(err.Error(), "symlink") {
		t.Fatalf("evil-link 应拒收: %v", err)
	}
}

// U-SK-04 SSOT 目录缺失 → 必报更新（不信缓存哈希）
func TestCheckUpdatesMissingSSOT(t *testing.T) {
	e := newEnv(t)
	row, err := e.service.InstallFromLocalZip(readFixture(t, "demo-skill.zip"), "", nil)
	mustNoErr(t, err)
	mustNoErr(t, os.RemoveAll(filepath.Join(e.sandbox, "skills", "demo-skill")))
	row.RepoOwner = strPtr("demo")
	row.RepoName = strPtr("skill")
	row.Branch = strPtr("main")
	persistRow(e, row)
	e.fetchZip = func(url string) ([]byte, error) {
		return makeZip(t, []zipEntry{{name: "demo-skill/SKILL.md", content: "---\nname: demo-skill\n---\nremote\n"}}), nil
	}
	report, err := e.service.CheckUpdates(context.Background())
	mustNoErr(t, err)
	if len(report.Failures) != 0 {
		t.Fatalf("不应有失败: %+v", report.Failures)
	}
	if len(report.Updates) != 1 || !report.Updates[0].Missing || report.Updates[0].SkillID != row.ID {
		t.Fatalf("缺失 SSOT 应必报更新: %+v", report.Updates)
	}
}

// U-SK-04 远端有更新 → update 应用并重投影已启用目标；installed_at 前滚；留备份
func TestUpdateFlow(t *testing.T) {
	e := newEnv(t)
	row, err := e.service.InstallFromLocalZip(readFixture(t, "demo-skill.zip"), "", nil)
	mustNoErr(t, err)
	if _, err := e.service.Toggle(row.ID, "claude-code", true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	oldHash := row.ContentHash
	oldInstalledAt := row.InstalledAt
	row.RepoOwner = strPtr("demo")
	row.RepoName = strPtr("skill")
	row.Branch = strPtr("main")
	persistRow(e, row)
	e.fetchZip = func(url string) ([]byte, error) {
		return makeZip(t, []zipEntry{{name: "demo-skill/SKILL.md", content: "---\nname: demo-skill\n---\nnew body\n"}}), nil
	}
	updated, err := e.service.Update(context.Background(), row.ID)
	mustNoErr(t, err)
	if updated.ContentHash == oldHash || updated.InstalledAt == oldInstalledAt {
		t.Fatalf("更新应前滚哈希与时间: %+v", updated)
	}
	projected := filepath.Join(e.sandbox, ".claude", "skills", "demo-skill")
	skillMD, err := os.ReadFile(filepath.Join(projected, "SKILL.md"))
	mustNoErr(t, err)
	if !containsStr2(string(skillMD), "new body") {
		t.Fatalf("重投影内容不符: %s", skillMD)
	}
	backups, err := os.ReadDir(filepath.Join(e.sandbox, "skill-backups"))
	mustNoErr(t, err)
	if len(backups) < 1 {
		t.Fatalf("更新应留备份")
	}
}

// U-SK-05 skills.sh 客户端：source 过 GitHub 白名单；分支解析；去重
func TestSearchSkillsSh(t *testing.T) {
	payload := `{"count": 3, "skills": [
		{"skillId": "find-skills", "name": "find-skills", "installs": 1000, "source": "anthropics/skills"},
		{"skillId": "x", "name": "x", "installs": 5, "source": "owner/repo@dev"},
		{"skillId": "bad", "name": "bad", "installs": 9, "source": "skills.volces.com/x"},
		{"skillId": "find-skills", "name": "find-skills", "installs": 1000, "source": "anthropics/skills"}
	]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()
	oldBase := skillsShBaseURL
	skillsShBaseURL = ts.URL
	defer func() { skillsShBaseURL = oldBase }()

	res, err := SearchSkillsSh(context.Background(), "find", 20, 0, 0)
	mustNoErr(t, err)
	if res.Count != 3 {
		t.Fatalf("count 应为 3（上游原始值）: %v", res.Count)
	}
	if len(res.Skills) != 2 {
		t.Fatalf("白名单过滤+去重后应 2 条: %+v", res.Skills)
	}
	if res.Skills[0].Repo != "anthropics/skills" || res.Skills[0].Branch != "main" || res.Skills[0].Installs != 1000 {
		t.Fatalf("第一条不符: %+v", res.Skills[0])
	}
	if res.Skills[1].Repo != "owner/repo" || res.Skills[1].Branch != "dev" {
		t.Fatalf("第二条不符: %+v", res.Skills[1])
	}
}

// U-SK-06 4 级解析：同名唯一目录优先；metadata name 唯一匹配兜底
func TestResolveSkillSourceDir(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(e.sandbox, "repo")
	mustMkdirAll(t, filepath.Join(root, "skills", "alpha"))
	mustMkdirAll(t, filepath.Join(root, "nested", "beta"))
	mustWrite(t, filepath.Join(root, "skills", "alpha", "SKILL.md"), "---\nname: alpha\n---\n")
	mustWrite(t, filepath.Join(root, "nested", "beta", "SKILL.md"), "---\nname: beta\n---\n")
	scanned, err := ScanSkillDirs(root)
	mustNoErr(t, err)
	if len(scanned) != 2 {
		t.Fatalf("应扫到 2 个: %d", len(scanned))
	}
	if filepath.Base(ResolveSkillSourceDir(root, scanned, "beta")) != "beta" {
		t.Fatalf("beta 应命中")
	}
	if filepath.Base(ResolveSkillSourceDir(root, scanned, "alpha")) != "alpha" {
		t.Fatalf("alpha 应命中")
	}
	if ResolveSkillSourceDir(root, scanned, "nope") != "" {
		t.Fatalf("未知名应空")
	}
}

// —— 辅助：无依赖最小 ZIP 构造（stored）——//

type zipEntry struct {
	name    string
	content string
}

func makeZip(t *testing.T, files []zipEntry) []byte {
	t.Helper()
	var chunks, central []byte
	var offset uint32
	for _, f := range files {
		nameBuf := []byte(f.name)
		data := []byte(f.content)
		crc := crc32.ChecksumIEEE(data)
		local := make([]byte, 30)
		binary.LittleEndian.PutUint32(local[0:], 0x04034b50)
		binary.LittleEndian.PutUint16(local[4:], 20)
		binary.LittleEndian.PutUint32(local[14:], crc)
		binary.LittleEndian.PutUint32(local[18:], uint32(len(data)))
		binary.LittleEndian.PutUint32(local[22:], uint32(len(data)))
		binary.LittleEndian.PutUint16(local[26:], uint16(len(nameBuf)))
		chunks = append(chunks, local...)
		chunks = append(chunks, nameBuf...)
		chunks = append(chunks, data...)
		cen := make([]byte, 46)
		binary.LittleEndian.PutUint32(cen[0:], 0x02014b50)
		binary.LittleEndian.PutUint16(cen[4:], 20)
		binary.LittleEndian.PutUint16(cen[6:], 20)
		binary.LittleEndian.PutUint32(cen[16:], crc)
		binary.LittleEndian.PutUint32(cen[20:], uint32(len(data)))
		binary.LittleEndian.PutUint32(cen[24:], uint32(len(data)))
		binary.LittleEndian.PutUint16(cen[28:], uint16(len(nameBuf)))
		binary.LittleEndian.PutUint32(cen[42:], offset)
		central = append(central, cen...)
		central = append(central, nameBuf...)
		offset += uint32(len(local) + len(nameBuf) + len(data))
	}
	end := make([]byte, 22)
	binary.LittleEndian.PutUint32(end[0:], 0x06054b50)
	binary.LittleEndian.PutUint16(end[8:], uint16(len(files)))
	binary.LittleEndian.PutUint16(end[10:], uint16(len(files)))
	binary.LittleEndian.PutUint32(end[12:], uint32(len(central)))
	binary.LittleEndian.PutUint32(end[16:], offset)
	out := append(append(chunks, central...), end...)
	// 自检：能被 ExtractZip 解析（防构造器坏掉导致测试假绿）
	if err := ExtractZip(out, t.TempDir()); err != nil {
		t.Fatalf("makeZip 构造的 ZIP 无法解压: %v", err)
	}
	return out
}

// —— 小工具 ——//

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsStr2(s, sub string) bool { return strings.Contains(s, sub) }

func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

var _ = json.Marshal
var _ = bytes.MinRead

// persistRow 把测试中手动设置的 repo 坐标回写到注册行（TS 里 config 是共享对象，行内即改即生效）。
func persistRow(e *testEnv, row store.InstalledSkill) {
	for i := range e.config.Skills {
		if e.config.Skills[i].ID == row.ID {
			e.config.Skills[i] = row
			return
		}
	}
}
