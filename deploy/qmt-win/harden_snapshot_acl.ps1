# harden_snapshot_acl.ps1 — §0929SECKEY-A：把"快照目录只留管理员可读"从口头纪律变成可判红的动作。
#
# 为什么要这个脚本（09-29 读码 + 现网实测锤实，不是文档转述）：
#   docs/RUNBOOK_QMT_DAILY.md §4.1b.13 之前写着灾备快照的补偿措施之一是"快照目录只留 Administrator
#   可读"。今天用 icacls 读现值，这句话是**错的**：
#     C:\var\lib\quant-snapshot   ← 快照暂存根（含 secrets\ 明文网关口令 + 两份库）
#       NT AUTHORITY\SYSTEM:(I)(OI)(CI)(F)
#       BUILTIN\Administrators:(I)(OI)(CI)(F)
#       BUILTIN\Users:(I)(OI)(CI)(RX)     ← 任意本机账号可读＝明文口令可读
#       BUILTIN\Users:(I)(CI)(AD)         ← 任意本机账号可在快照根建目录
#       BUILTIN\Users:(I)(CI)(WD)         ← 任意本机账号可在快照根建文件（可种假 SNAPSHOT_OK）
#     C:\var\lib\quant-restic-repo        ← 同一套默认 ACE（Mac 拉取腿经 sftp 读的就是它）
#   "文档写着的补偿措施其实一条都没落地"这一族，本仓已有先例（§BOM-REPO：仓库字节 BOM 锁只点名
#   两个文件；§ENH-5：部署清单漏列执行体）。所以本脚本把收敛做成**可执行、可自检、可回滚**的一步，
#   并由 verify 探针把"还留着白名单外 ACE"变成判红项，而不是继续当纪律。
#
# 三个判据（缺一条这脚本就不该存在）：
#   ① 动手前先 `icacls /save` 存回滚凭证——存不下来就直接抛，不允许"边试边改"；
#   ② 收敛后必须**真拨一次入口**：读一个真实存在文件 + 在目标目录写一个自测文件再删掉，
#      证明没把夜任务（SYSTEM）和 Mac 拉取腿（实测 ssh 身份是本机 administrator，
#      属 BUILTIN\Administrators 成员）掐死；只改完打印"成功"不算通过（§装入口必须真拨一次）；
#   ③ 白名单之外的 ACE 一律视为失败。拉取腿若哪天改用非管理员账号，用 `-ExtraSids` 显式加进去，
#      不允许靠"留着 Users 兜底"蒙过判红。
#
# 缺省 **只读预演**（不带 -Apply 一个字节都不改），与 rotate_qmt_token.ps1 / decommission 家族同姿势。
# 只动这三处：快照根、restic 中转仓、restic 口令文件。**不碰** C:\var\lib\quant-trading-v2
#   （引擎数据目录，服务账号在读）也不碰 C:\etc\quant.env（NSSM 服务 env 源），那是另一条权限面。
# 含中文注释 ⇒ 文件必须带恰好一个 UTF-8 BOM（部署链 ps1_bom 归一，仓库字节由门禁 §70 派生锁自检）。
#
# 用法（管理员 PowerShell，现网路径）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\deploy\qmt-win\harden_snapshot_acl.ps1            # 预演：只报现状，白名单外 ACE 存在则退出码 1
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\deploy\qmt-win\harden_snapshot_acl.ps1 -Apply    # 真收敛（先存回滚凭证，再自检读/写）
param(
    [switch]$Apply,
    [string]$ExtraSids = ""      # 逗号分隔的额外允许 SID（如拉取腿改用普通账号时）
)
$ErrorActionPreference = "Stop"

# 三个目标路径与 deploy/qmt-win/backup_snapshot.ps1 里的字面量**同源**
#   （$SnapRoot / $RepoDir / $Pass）：门禁 §106 用交叉等值锁钉死，任何一侧改字面量即红，
#   防止"收敛了一个目录、备份脚本写到另一个目录"这种双份实现漂移。
$SnapRoot = "C:\var\lib\quant-snapshot"
$RepoDir  = "C:\var\lib\quant-restic-repo"
$PassFile = "C:\opt\quant\tools\restic-pass.txt"

# 白名单：SYSTEM（夜任务身份）+ BUILTIN\Administrators（人工与拉取腿身份）。
$AllowedSids = @("S-1-5-18", "S-1-5-32-544")
if ($ExtraSids) {
    foreach ($s in ($ExtraSids -split ',')) {
        $t = $s.Trim()
        if ($t -match '^S-\d+(-\d+)+$') { $AllowedSids += $t }
        else { throw "ExtraSids 里 '${t}' 不是合法 SID（必须形如 S-1-5-XX，不接受账号名——账号名会随机器域变化）" }
    }
}
$AllowedSids = @($AllowedSids | Sort-Object -Unique)

function Get-AceSids($path) {
    # 判据一律取 SID，绝不解析 icacls 的文本输出：那条输出按控制台码页本地化账号名，
    # 经 ssh 回传会变 GBK 乱码、且"Administrators"在中文系统显示成本地化名（§GBK 回传教训）。
    $acl = Get-Acl -Path $path
    $sids = New-Object System.Collections.Generic.List[string]
    foreach ($ace in $acl.Access) {
        try {
            $sid = $ace.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
        } catch {
            # 翻译不了（已删除的账号留下的孤儿 ACE）也要进判据：它同样是一条访问许可，
            # 当成不存在就是自证绿。标成 UNTRANSLATABLE 让白名单检查必然判红。
            $sid = "UNTRANSLATABLE:$($ace.IdentityReference.Value)"
        }
        $sids.Add($sid)
    }
    return @($sids | Sort-Object -Unique)
}

function Get-Outside($sids) {
    return @($sids | Where-Object { $AllowedSids -notcontains $_ })
}

# 预演用：把一条目标的现状打成一行了事，绝不改字节。返回 @{sids;outside;exists} 供汇总判红。
function Report-Target($label, $path) {
    if (!(Test-Path $path)) {
        Write-Output ("ACL|target=" + $label + " state=absent path=" + $path)
        return $null
    }
    $sids = Get-AceSids $path
    $out = Get-Outside $sids
    Write-Output ("ACL|target=" + $label + " path=" + $path + " sids=" + ($sids -join ';') + " outside=" + $out.Count)
    return @{ sids = $sids; outside = $out; exists = $true }
}

function Protect-Dir($label, $path) {
    # ① 回滚凭证先落盘：icacls /save 记下当前 DACL，出问题用 `icacls <path> /restore <bak>` 还原。
    $bak = Join-Path $env:TEMP ("acl-" + $label + "-" + (Get-Date -Format "yyyyMMdd-HHmmss") + ".bak")
    & icacls $path /save $bak | Out-Null
    if ($LASTEXITCODE -ne 0 -or !(Test-Path $bak)) {
        throw ("回滚凭证保存失败（rc=" + $LASTEXITCODE + "），拒绝在无还原手段的情况下改权限：" + $path)
    }
    $args2 = @($path, "/inheritance:r", "/grant:r")
    foreach ($sid in $AllowedSids) { $args2 += ("*{0}:(OI)(CI)F" -f $sid) }
    & icacls @args2 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw ("icacls 收敛失败 rc=" + $LASTEXITCODE + " path=" + $path + "（回滚凭证 " + $bak + "）") }

    # ③ 收敛后判据：白名单外一条都不许留。
    $after = Get-AceSids $path
    $out = Get-Outside $after
    if ($out.Count -gt 0) {
        throw ("收敛后仍有白名单外 ACE：" + ($out -join ';') + " path=" + $path + "（回滚凭证 " + $bak + "）")
    }
    # ② 真拨一次入口：读一个真实产物 + 目录内写删一次（证明 SYSTEM/管理员这条腿还活着，
    #    也证明 Mac 拉取腿要读的文件仍在原处）。写删用自测文件名，绝不碰快照内容。
    $marker = Join-Path $path "SNAPSHOT_OK"
    if (Test-Path $marker) { [void][System.IO.File]::ReadAllText($marker) }
    $probe = Join-Path $path (".acl-selftest-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
    [System.IO.File]::WriteAllText($probe, "acl selftest")
    Remove-Item $probe -Force
    Write-Output ("ACL_APPLIED|target=" + $label + " path=" + $path + " sids=" + ($after -join ';') + " selftest=read+write-ok rollback=" + $bak)
}

function Protect-Pass($label, $path) {
    if (!(Test-Path $path)) { Write-Output ("ACL|target=" + $label + " state=absent path=" + $path); return }
    $bak = Join-Path $env:TEMP ("acl-" + $label + "-" + (Get-Date -Format "yyyyMMdd-HHmmss") + ".bak")
    & icacls $path /save $bak | Out-Null
    if ($LASTEXITCODE -ne 0 -or !(Test-Path $bak)) { throw ("回滚凭证保存失败（rc=" + $LASTEXITCODE + "）：" + $path) }
    # 口令文件不需要继承来的 Users：整体断开继承，只留白名单（文件不加 (OI)(CI)）。
    # 其中 SYSTEM 是**夜任务真正读它的那个身份**——现网此前只靠 LocalSystem 的隐式特权读得到，
    # 那是"能跑但不是授权"；显式写上以后，换成受限账号跑任务会如实失败，而不是悄悄依赖特权。
    & icacls $path /inheritance:r | Out-Null
    if ($LASTEXITCODE -ne 0) { throw ("icacls 断开继承失败 rc=" + $LASTEXITCODE + " path=" + $path + "（回滚凭证 " + $bak + "）") }
    $gargs = @($path, "/grant:r")
    foreach ($sid in $AllowedSids) { $gargs += ("*{0}:F" -f $sid) }
    & icacls @gargs | Out-Null
    if ($LASTEXITCODE -ne 0) { throw ("icacls 收敛失败 rc=" + $LASTEXITCODE + " path=" + $path + "（回滚凭证 " + $bak + "）") }
    $after = Get-AceSids $path
    $out = Get-Outside $after
    if ($out.Count -gt 0) { throw ("口令文件仍有白名单外 ACE：" + ($out -join ';') + "（回滚凭证 " + $bak + "）") }
    # ② 真拨一次：夜任务的读法就是按路径读文件内容，这里用同样方式读一次（只数字节数，绝不回显内容）。
    $len = ([System.IO.File]::ReadAllText($path)).Length
    Write-Output ("ACL_APPLIED|target=" + $label + " path=" + $path + " sids=" + ($after -join ';') + " selftest=read-ok bytes=" + $len + " rollback=" + $bak)
}

Write-Output ("ACL|mode=" + ($(if ($Apply) { "apply" } else { "preview" })) + " allowed=" + ($AllowedSids -join ';'))

$targets = @(
    @{ label = "snap_root"; path = $SnapRoot; dir = $true },
    @{ label = "restic_repo"; path = $RepoDir; dir = $true },
    @{ label = "restic_pass"; path = $PassFile; dir = $false }
)

$outsideTotal = 0
foreach ($t in $targets) {
    $r = Report-Target $t.label $t.path
    if ($null -eq $r) { continue }
    $outsideTotal += $r.outside.Count
    if ($Apply) {
        if ($t.dir) { Protect-Dir $t.label $t.path } else { Protect-Pass $t.label $t.path }
    }
}

if ($Apply) {
    # 收敛完立即复核一遍（写侧自证不等于运行态吃到——这里用重读而不是相信刚才的返回值）。
    $still = 0
    foreach ($t in $targets) {
        if (!(Test-Path $t.path)) { continue }
        $still += (Get-Outside (Get-AceSids $t.path)).Count
    }
    if ($still -gt 0) { throw ("收敛后复核仍有 " + $still + " 条白名单外 ACE（见上方回滚凭证）") }
    Write-Output "ACL_RESULT|ok=true outside=0"
    exit 0
}

# 预演模式：把"今天到底裸不裸"如实回显成退出码——现网实测 outside>0 时这条命令必红，
# 不写"永远绿"的观测行（§DEADGAUGE 同族：观测值必须能导致判红）。
if ($outsideTotal -gt 0) {
    Write-Output ("ACL_RESULT|ok=false outside=" + $outsideTotal + " verdict=快照/口令目录对白名单外账号开放，跑 -Apply 收敛")
    exit 1
}
Write-Output "ACL_RESULT|ok=true outside=0"
exit 0
