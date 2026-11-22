# VENU-27 原生面板回归验证（Windows）：一条命令跑完四个场景并给出 PASS/FAIL。
#
#   pwsh -File scripts/verify-native-panel.ps1
#   pwsh -File scripts/verify-native-panel.ps1 -Exe release/v0.1.0/beaver.exe -Port 16397 -Screenshot
#
# 场景：1 首次双击（托盘+原生窗口+daemon）2 二次双击单实例 3 陈旧 pid + PID 复用
#       4 daemon 不可达时显示错误页而非白屏。
# 全程使用系统临时目录 + 独立端口，不碰用户的 ~/.beaver 与 6280。

param(
    [string]$Exe = "",
    [int]$Port = 16397,
    [switch]$Screenshot
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if (-not $Exe) { $Exe = Join-Path $root "release\v0.1.0\beaver.exe" }
if (-not (Test-Path $Exe)) { throw "找不到 exe: $Exe（先运行 scripts/build-release.ps1）" }
$Exe = (Resolve-Path $Exe).Path

# 隔离前置条件：本机不能有别的 beaver 在跑。面板按窗口标题全局查找，
# 外部实例会让「daemon 不可达 → 错误页」场景直接聚焦到别人的窗口而误报 FAIL。
$foreign = @(Get-CimInstance Win32_Process -Filter "name LIKE 'beaver%'" -ErrorAction SilentlyContinue)
if ($foreign.Count -gt 0) {
    throw "请先退出本机正在运行的 beaver（pid: $($foreign.ProcessId -join ',')）再跑回归，否则场景 4 会被已有面板窗口干扰"
}

$base = Join-Path $env:TEMP ("beaver-verify-" + (Get-Date -Format "yyyyMMddHHmmss"))
$homeDir = Join-Path $base "home"
$agents = Join-Path $base "agents"
$home2 = Join-Path $base "home2"
New-Item -ItemType Directory -Force -Path $homeDir, $agents, $home2 | Out-Null

$failures = @()
function Pass($m) { Write-Host "PASS  $m" -ForegroundColor Green }
function Fail($m) { Write-Host "FAIL  $m" -ForegroundColor Red; $script:failures += $m }
function Note($m) { Write-Host "      $m" }
function Status() {
    try { return Invoke-RestMethod -Uri "http://127.0.0.1:$Port/api/status" -TimeoutSec 3 } catch { return $null }
}
function BeaverProcs() { @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.Name -like "beaver*" -and $_.CommandLine -like "*$base*" }) }
function KillScoped() {
    foreach ($p in (BeaverProcs)) { try { Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop } catch {} }
}
function PanelLog($dir) {
    $p = Join-Path $dir "logs\panel.log"
    if (Test-Path $p) { return (Get-Content $p -Encoding UTF8) -join "`n" }
    return ""
}

if ($Screenshot) {
    Add-Type -AssemblyName System.Drawing
    $sig = @'
using System;
using System.Runtime.InteropServices;
public class Win32VerifyShot {
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr hwnd, IntPtr hdcBlt, uint nFlags);
  [DllImport("user32.dll")] public static extern IntPtr FindWindow(string lpClassName, string lpWindowName);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hwnd, out RECT rect);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
}
'@
    Add-Type -TypeDefinition $sig
    function Shot($name) {
        $hwnd = [Win32VerifyShot]::FindWindow("webview", "Beaver（河狸）")
        if ($hwnd -eq [IntPtr]::Zero) { $hwnd = [Win32VerifyShot]::FindWindow($null, "Beaver（河狸）") }
        if ($hwnd -eq [IntPtr]::Zero) { Note "截图跳过：未找到面板窗口"; return }
        $rect = New-Object Win32VerifyShot+RECT
        [void][Win32VerifyShot]::GetWindowRect($hwnd, [ref]$rect)
        $w = $rect.Right - $rect.Left; $h = $rect.Bottom - $rect.Top
        $bmp = New-Object System.Drawing.Bitmap $w, $h
        $g = [System.Drawing.Graphics]::FromImage($bmp)
        $hdc = $g.GetHdc()
        [void][Win32VerifyShot]::PrintWindow($hwnd, $hdc, 2)
        $g.ReleaseHdc($hdc); $g.Dispose()
        $p = Join-Path $base $name
        $bmp.Save($p, [System.Drawing.Imaging.ImageFormat]::Png)
        $bmp.Dispose()
        Note "截图: $p ($((Get-Item $p).Length) bytes)"
    }
}

try {
    Write-Host "exe=$Exe home=$homeDir port=$Port"

    # 1) 首次双击
    Start-Process -FilePath $Exe -ArgumentList @("--home", $homeDir, "--agent-home", $agents, "--port", $Port)
    Start-Sleep -Seconds 6
    $s = Status
    if ($s -and $s.port -eq $Port -and $s.homeDir) {
        Pass "首次启动: /api/status 200, port=$($s.port), version=$($s.version), commit=$($s.commit)"
    } else {
        Fail "首次启动: /api/status 不可用或端口不符（$(if ($s) { $s.port } else { '无响应' })）"
    }
    $title = (Get-Process beaver -ErrorAction SilentlyContinue | Where-Object { $_.MainWindowTitle } | Select-Object -First 1).MainWindowTitle
    if ($title -eq "Beaver（河狸）") { Pass "原生面板窗口存在（标题 $title）" } else { Fail "未找到标题为 Beaver（河狸） 的窗口（实际: $title）" }
    $procCount = (BeaverProcs).Count
    if ($procCount -ge 2) { Pass "进程模型: 托盘 + __panel（$procCount 个）" } else { Fail "进程数不足: $procCount" }
    # 控制台残留断言（VENU-43）：双击后不得再挂一个 cmd / Windows Terminal 日志窗。
    # 控制台子系统 exe 会被系统分配控制台；默认终端为 Windows Terminal 时是 ConPTY，
    # ShowWindow 关不掉标签页，旧实现因此「面板 + 一个日志终端」并存。
    # 以 tray.pid 为准枚举子进程：按命令行匹配会漏（exe 改名时进程名也变）。
    $trayNow = 0
    $pidFile = Join-Path $homeDir "tray.pid"
    if (Test-Path $pidFile) { $trayNow = [int](Get-Content $pidFile) }
    if ($trayNow -gt 0 -and (Get-Process -Id $trayNow -ErrorAction SilentlyContinue)) {
        $panelNow = (Get-CimInstance Win32_Process -Filter "ParentProcessId=$trayNow" -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -like "beaver*" } | Select-Object -First 1).ProcessId
        $kids = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
            ($_.ParentProcessId -eq $trayNow -or $_.ParentProcessId -eq $panelNow) -and
            ($_.Name -eq "conhost.exe" -or $_.Name -eq "OpenConsole.exe")
        })
        if ($kids.Count -eq 0) {
            Pass "无控制台残留（托盘 $trayNow / 窗口 $panelNow 下没有 conhost/OpenConsole）"
        } else {
            Fail "残留控制台窗口: $($kids.ProcessId -join ',')（宿主 pid $trayNow）"
        }
    } else {
        Fail "无法断言控制台残留：tray.pid 缺失或进程已退出"
    }
    if ($Screenshot) { Shot "01-first-launch.png" }

    # 2) 二次双击：单实例
    Start-Process -FilePath $Exe -ArgumentList @("--home", $homeDir, "--agent-home", $agents, "--port", $Port)
    Start-Sleep -Seconds 3
    $procCount2 = (BeaverProcs).Count
    if ($procCount2 -eq $procCount) { Pass "二次双击不新开进程（$procCount2 个）" } else { Fail "二次双击后进程数变化: $procCount → $procCount2" }

    # 3) 陈旧 pid + PID 复用
    $trayPid = [int](Get-Content (Join-Path $homeDir "tray.pid"))
    foreach ($p in (BeaverProcs | Where-Object { $_.CommandLine -match "__panel" })) { try { Stop-Process -Id $p.ProcessId -Force } catch {} }
    Start-Sleep -Seconds 1
    try { Stop-Process -Id $trayPid -Force } catch {}
    Start-Sleep -Seconds 3
    if (Status) { Fail "强杀托盘后 daemon 仍在监听（预期端口已释放）" }

    $decoy = Start-Process -FilePath (Join-Path $env:SystemRoot "System32\cmd.exe") `
        -ArgumentList "/c", "ping -n 120 127.0.0.1 >nul" -PassThru -WindowStyle Hidden
    Set-Content -Path (Join-Path $homeDir "tray.pid") -Value $decoy.Id
    Set-Content -Path (Join-Path $homeDir "daemon.pid") -Value $decoy.Id
    Start-Process -FilePath $Exe -ArgumentList @("--home", $homeDir, "--agent-home", $agents, "--port", $Port)
    Start-Sleep -Seconds 7
    $s = Status
    if ($s -and $s.port -eq $Port) {
        Pass "陈旧 pid（活着的无关进程 $($decoy.Id)）后双击: daemon 正常就绪 port=$($s.port)"
    } else {
        Fail "陈旧 pid 后双击失败（静默白屏回归）: status=$(if ($s) { '端口不符' } else { '无响应' })"
    }
    $log = PanelLog $homeDir
    if ($log -match "陈旧") { Pass "panel.log 记录了陈旧 pid 判定" } else { Fail "panel.log 缺少陈旧 pid 记录" }
    if ($Screenshot) { Shot "02-stale-pid.png" }

    # 4) daemon 不可达 → 错误页
    $cur = (Get-Content (Join-Path $homeDir "tray.pid") -ErrorAction SilentlyContinue)
    if ($cur) { try { Stop-Process -Id ([int]$cur) -Force } catch {} }
    foreach ($p in (BeaverProcs | Where-Object { $_.CommandLine -match "__panel" })) { try { Stop-Process -Id $p.ProcessId -Force } catch {} }
    Start-Sleep -Seconds 2
    $env:BEAVER_PANEL_URL = "http://127.0.0.1:1"
    Start-Process -FilePath $Exe -ArgumentList @("__panel", "--home", $home2)
    Start-Sleep -Seconds 6
    $log2 = PanelLog $home2
    if ($log2 -match "不可达") { Pass "panel.log 记录了 daemon 不可达" } else { Fail "panel.log 缺少 daemon 不可达记录" }
    if (Test-Path (Join-Path $home2 "webview2\panel-error.html")) {
        Pass "daemon 不可达时生成错误页（不再白屏）"
    } else {
        Fail "daemon 不可达时未生成错误页"
    }
    if ($Screenshot) { Shot "03-error-page.png" }
    Remove-Item Env:\BEAVER_PANEL_URL -ErrorAction SilentlyContinue
} finally {
    KillScoped
    try { Stop-Process -Id $decoy.Id -Force -ErrorAction SilentlyContinue } catch {}
    Start-Sleep -Seconds 2
    Note "artifacts: $base"
}

Write-Host ""
if ($failures.Count -eq 0) {
    Write-Host "RESULT: PASS" -ForegroundColor Green
    exit 0
}
Write-Host "RESULT: FAIL ($($failures.Count))" -ForegroundColor Red
exit 1