# 构建可分发单文件 exe：注入版本与 commit，输出到 release/v<version>/，
# 并打印 sha256 供交付核对（血脉：exe ↔ commit）。
#
# 用法（仓库根）：
#   pwsh -File scripts/build-release.ps1
#   pwsh -File scripts/build-release.ps1 -Version 0.1.0 -Zip
#
# 注意：前端改动必须先 `pnpm --filter @beaver/web build` +
# `node scripts/sync-web-dist.mjs`，让 internal/webui/dist 与源码一致。
param(
    [string]$Version = "0.1.0",
    [switch]$Zip
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $commit = (& git rev-parse --short=12 HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw "无法读取 git commit" }
    $dirty = (& git status --porcelain).Trim()
    if ($dirty) {
        Write-Warning "工作区有未提交改动：产物对应的 commit 不完整，请先提交"
    }

    $outDir = Join-Path $root (Join-Path "release" "v$Version")
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null
    $exe = Join-Path $outDir "beaver.exe"

    $versionPkg = "github.com/hxgh776/beaver/internal/version"
    $ldflags = "-s -w -X $versionPkg.value=$Version -X $versionPkg.commit=$commit"
    Write-Host "构建 beaver v$Version ($commit) -> $exe"
    & go build -trimpath -buildvcs=true -ldflags $ldflags -o $exe ./cmd/beaver
    if ($LASTEXITCODE -ne 0) { throw "go build 失败" }

    $hash = (Get-FileHash $exe -Algorithm SHA256).Hash.ToLower()
    $size = (Get-Item $exe).Length
    $info = & go version -m $exe 2>$null | Out-String
    $vcs = ($info -split "`n" | Where-Object { $_ -match "vcs.revision" }) -join ""
    Write-Host ""
    Write-Host "exe    : $exe"
    Write-Host "bytes  : $size"
    Write-Host "sha256 : $hash"
    Write-Host "commit : $commit"
    Write-Host ($vcs.Trim())

    if ($Zip) {
        $zipPath = Join-Path $outDir "beaver-v$Version-windows-x64-native.zip"
        if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
        Compress-Archive -Path $exe -DestinationPath $zipPath
        $zipHash = (Get-FileHash $zipPath -Algorithm SHA256).Hash.ToLower()
        Write-Host "zip    : $zipPath"
        Write-Host "zip sha: $zipHash"
    }
} finally {
    Pop-Location
}