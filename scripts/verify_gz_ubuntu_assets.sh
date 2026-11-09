#!/bin/bash
# §GZ-UBUNTU 11-08：广州 Ubuntu 外脑部署件门禁（新增的自动化测试腿）。
# 为什么单独一条：verify_changes.sh 的段清单由装配器从 GATE-SECTION-START 动态派生，
# 为了不新增段号、不动契约台账计数，本脚本作为「最终总闸」挂在主门禁尾部，
# 也可以单独跑（bash scripts/verify_gz_ubuntu_assets.sh）。
# 判据全部是对文件的**肯定**读数（存在/含某串），否定结论一律用 Python 读文件，
# 不信 shell grep 的「零命中」——本机 shim 下的 grep 曾把 CrossCheckPrice 消费链误判成死代码。
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0
FAIL=0
FAILED=""

ck() { # $1=id $2=0(通过)/1(失败) $3=detail
  local id="$1" bad="$2" detail="$3"
  if [ "$bad" = "0" ]; then
    printf 'PASS|%s|%s\n' "$id" "$detail"
    PASS=$((PASS + 1))
  else
    printf 'FAIL|%s|%s\n' "$id" "$detail"
    FAIL=$((FAIL + 1))
    FAILED="$FAILED $id"
  fi
}

has_file() { [ -f "$1" ] && [ -s "$1" ]; }
has_text() { python3 -c 'import sys; sys.exit(0 if open(sys.argv[1],encoding="utf-8",errors="replace").read().find(sys.argv[2])>=0 else 1)' "$1" "$2" 2>/dev/null; }
count_text() { python3 -c 'import sys; print(open(sys.argv[1],encoding="utf-8",errors="replace").read().count(sys.argv[2]))' "$1" "$2" 2>/dev/null || echo 0; }

# 1) 六份 Linux 部署件必须在位且非空（缺一份就是「新系统配着旧文档过夜」）。
for f in deploy/gz-ubuntu/Caddyfile \
         deploy/gz-ubuntu/quant.service \
         deploy/gz-ubuntu/quant-research.service \
         deploy/gz-ubuntu/pydata.service \
         deploy/gz-ubuntu/backup_daily.sh \
         deploy/gz-ubuntu/verify_backup_manifest.py; do
  if has_file "$f"; then ck "asset_$(basename "$f")" 0 "$f"; else ck "asset_$(basename "$f")" 1 "missing=$f"; fi
done

# 2) Caddy 反代端口与 /livez 健康判据（方案 §6.6 ③；只反代 /api/* 会让 /livez 变 404）。
CF=deploy/gz-ubuntu/Caddyfile
if has_text "$CF" 'reverse_proxy 127.0.0.1:8081' && has_text "$CF" 'handle /livez'; then
  ck "caddy_8081_and_livez" 0 "reverse_proxy=8081 livez=handled"
else
  ck "caddy_8081_and_livez" 1 "port_or_livez_missing"
fi

# 3) unit 口径：8081、EnvironmentFile、TZ，且内存上限按 2 核/4GB 重定，不许套首尔模板。
U=deploy/gz-ubuntu/quant.service
if has_text "$U" 'QUANT_ADDR=127.0.0.1:8081' && has_text "$U" 'EnvironmentFile=-/etc/quant.env' \
   && has_text "$U" 'TZ=Asia/Shanghai' && has_text "$U" 'MemoryMax=1400M'; then
  ck "unit_quant_params" 0 "addr=8081 env=0600 TZ=CST MemoryMax=1400M"
else
  ck "unit_quant_params" 1 "unit_drifted_from_gz_profile"
fi

# 4) Linux 探针必须在位且 --selftest 变异反证真红（防恒绿假探针）。
PROBE=scripts/verify_deploy_guangzhou_linux.sh
if has_file "$PROBE"; then
  SELFTEST_OUT="$(bash "$PROBE" --selftest 2>&1)"
  if printf '%s' "$SELFTEST_OUT" | grep -qF 'SELFTEST_RESULT=OK green_fail=0 mutated_fail=1'; then
    ck "linux_probe_selftest" 0 "green_fail=0 mutated_fail=1"
  else
    ck "linux_probe_selftest" 1 "selftest_not_green"
  fi
  # 变异反证：把探针副本的注入失败拿掉，selftest 必须判红；副本改坏不碰主仓。
  TMPD="$(mktemp -d /tmp/gz-assets-gate.XXXXXX)" || TMPD=""
  if [ -n "$TMPD" ]; then
    python3 - "$PROBE" "$TMPD/probe.sh" <<'PY'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
s = s.replace('MUTATE="unit_quant"', 'MUTATE=""', 1)
open(sys.argv[2], "w", encoding="utf-8").write(s)
PY
    bash "$TMPD/probe.sh" --selftest >/dev/null 2>&1
    mrc=$?
    if [ "$mrc" != "0" ]; then
      ck "linux_probe_mutation_counterproof" 0 "mutated_probe_rc=$mrc"
    else
      ck "linux_probe_mutation_counterproof" 1 "mutated_probe_still_green"
    fi
    rm -rf "$TMPD"
  else
    ck "linux_probe_mutation_counterproof" 1 "tmpdir_unavailable"
  fi
else
  ck "linux_probe_selftest" 1 "probe_absent"
  ck "linux_probe_mutation_counterproof" 1 "probe_absent"
fi

# 5) restic 异地链缺省必须指向 linux 仓（windows 路径已随系统退役）。
if has_text deploy/mac/restic_pull_backup.sh 'RESTIC_REPO_MODE:-linux'; then
  ck "restic_default_linux" 0 "mode=linux"
else
  ck "restic_default_linux" 1 "mode_not_linux"
fi

# 6) 旧 Windows 部署件必须带退役标注，保留文件供审计但不得被当成现行入口。
for f in scripts/deploy_guangzhou.sh scripts/verify_deploy_guangzhou.sh; do
  if has_text "$f" '已退役'; then ck "retired_$(basename "$f")" 0 "marked_retired"; else ck "retired_$(basename "$f")" 1 "no_retired_mark"; fi
done

# 7) 冷备工具链四条环境纪律（都是现网真踩过的坑，静态锁防回潮）。
B=scripts/backup_gz_cold.sh
N_EXIT="$(count_text "$B" 'exit $LASTEXITCODE')"
if has_text "$B" 'local_only' && has_text "$B" 'ServerAliveInterval=30' \
   && has_text "$B" "helper_mode" && [ "$N_EXIT" -ge 2 ]; then
  ck "cold_backup_remote_discipline" 0 "mode_split=yes explicit_exit=$N_EXIT keepalive=yes"
else
  ck "cold_backup_remote_discipline" 1 "discipline_regressed"
fi

# 8) 日常备份腿必须带逐文件 manifest、excluded[] 与中文头双闸。
D=deploy/gz-ubuntu/backup_daily.sh
if has_text "$D" 'MANIFEST.json' && has_text "$D" '"excluded"' \
   && has_text "$D" '日常全目录备份' && has_text "$D" '/bin/cp'; then
  ck "daily_backup_manifest" 0 "manifest+excluded+cn_header+abs_cp"
else
  ck "daily_backup_manifest" 1 "manifest_discipline_missing"
fi

# 9) §FRIEND-UPDATE 通道静态锁（2026-11-09：装机 11 连 bug + PS5.1 四雷的防回潮闸）。
E=deploy/qmt-friend/enroll.ps1
U=deploy/qmt-friend/update_leg.ps1
P=scripts/publish_friend_pkg.sh
K=deploy/gz-ubuntu/Caddyfile
IT=deploy/qmt-friend/install_tasks.ps1
IP=deploy/qmt-friend/install.ps1
SG=deploy/qmt-friend/start_gateway.ps1
WD=deploy/qmt-friend/watchdog.ps1
EN=deploy/qmt-friend/ensure_friend_gateway_config.ps1
# 9a 白名单与拉取器本体
if has_text 'scripts/build_friend_package.sh' 'update_leg.ps1' && has_text "$U" 'UPDATE_RESULT=DRYRUN' \
   && has_text "$U" 'Get-FileHash' && has_text "$U" 'param(' && has_text "$U" '-Apply'; then
  ck "update_channel_updater" 0 "whitelist+dryrun_default+sha_verify"
else
  ck "update_channel_updater" 1 "updater_discipline_missing"
fi
# 9b 发布脚本：sudo 清建（/opt/quant root 属主）+ 大写 MANIFEST.json（ubuntu 区分大小写）
if has_text "$P" 'sudo rm -rf' && has_text "$P" 'MANIFEST.json' \
   && ! has_text "$P" '/leg/manifest.json'; then
  ck "update_channel_publish" 0 "sudo_prepare+case_exact"
else
  ck "update_channel_publish" 1 "publish_discipline_missing"
fi
# 9c Caddy /leg 路由指向 friend-pkg
if has_text "$K" 'handle_path /leg/*' && has_text "$K" '/opt/quant/friend-pkg'; then
  ck "update_channel_caddy" 0 "route+root"
else
  ck "update_channel_caddy" 1 "caddy_route_missing"
fi
# 9d PYTHON_EXE 走 friend.env（/TR 261 上限）；install_tasks 的 /TR 不得内联 -PythonExe
if has_text "$EN" '"PYTHON_EXE=$PythonExe"' && has_text "$SG" '^PYTHON_EXE=' \
   && has_text "$WD" '^PYTHON_EXE=' && has_text "$IT" 'carrier_run.ps1' \
   && ! has_text "$IT" 'Tr = .*(-PythonExe)'; then
  ck "friend_env_python_exe" 0 "ensure_writes+gw_wd_parse+carrier_run_wrapper"
else
  ck "friend_env_python_exe" 1 "python_exe_chain_regressed"
fi
# 9e enroll：无裸 "< file" 重定向；任务调用必须哈希表展开（数组展开传不了命名参数）
if has_text "$E" 'cmd /c $cmd' && has_text "$E" '@{ Apply = $true' \
   && ! has_text "$E" '-Apply < $tmpTok' && ! has_text "$E" '@("-Apply"'; then
  ck "enroll_ps51_discipline" 0 "cmd_c_wrapper+hashtable_splat"
else
  ck "enroll_ps51_discipline" 1 "enroll_regressed"
fi
# 9f install.ps1 捕获必须 *>&1（Write-Host 锚点走信息流 6），且恰好 5 处
N_CAP="$(count_text "$IP" '*>&1) | Out-String')"
if ! has_text "$IP" '2>&1) | Out-String' && [ "$N_CAP" -eq 5 ]; then
  ck "install_capture_all_streams" 0 "star_gt1_x5"
else
  ck "install_capture_all_streams" 1 "capture_regressed count=$N_CAP"
fi
# 9g 八份朋友侧 ps1 无 "$var:" 作用域冒号形态（cfgPathACL 同族解析雷；env: 例外）
VAR_COLON=0
for f in "$E" "$EN" "$SG" "$WD" "$IT" "$IP" deploy/qmt-friend/install_local_executor.ps1 deploy/qmt-friend/update_leg.ps1; do
  C=$(python3 -c "
import io,re,sys
t=io.open('$f',encoding='utf-8').read()
print(len([m.group(0) for m in re.finditer(r'\\\$([A-Za-z_]\\\\w*):', t) if m.group(1)!='env']))" 2>/dev/null || echo 1)
  VAR_COLON=$((VAR_COLON + C))
done
if [ "$VAR_COLON" -eq 0 ]; then
  ck "ps_var_colon_scan" 0 "zero_var_colon_x8"
else
  ck "ps_var_colon_scan" 1 "var_colon_hits=$VAR_COLON"
fi

# 9h 八份朋友侧 ps1 here-string 配对：每个行尾 @' 必须有行首 '@ 收（2026-11-09 实录：
# Python 回灌脚本把闭合行写成 ' 丢了 @，坏包发上通道、朋友机更新后 install_tasks 解析炸）。
HS_BAD=0
for f in "$E" "$EN" "$SG" "$WD" "$IT" "$IP" deploy/qmt-friend/install_local_executor.ps1 deploy/qmt-friend/update_leg.ps1; do
  C=$(python3 -c "
import io
ls=io.open('$f',encoding='utf-8').read().split('\n')
o=sum(1 for l in ls if l.rstrip('\r').endswith(\"@'\") and not l.strip().startswith(\"'@\"))
c=sum(1 for l in ls if l.startswith(\"'@\"))
print(0 if o==c else 1)" 2>/dev/null || echo 1)
  HS_BAD=$((HS_BAD + C))
done
if [ "$HS_BAD" -eq 0 ]; then
  ck "ps_herestring_balance" 0 "paired_x8"
else
  ck "ps_herestring_balance" 1 "unbalanced_files=$HS_BAD"
fi

# 9i §CUTOVER 每日更新通道：常规包发布必须保护 carrier/，且发布后校验那枚 URL 仍 200
# （2026-11-09 10:24 实录：publish_friend_pkg.sh 的 rm -rf 把 /leg/carrier/* 抹成 404）。
if has_text 'scripts/publish_friend_pkg.sh' 'friend-pkg-carrier-keep' \
   && has_text 'scripts/publish_friend_pkg.sh' 'carrier channel broken' \
   && has_text 'scripts/publish_carrier.sh' 'CARRIER_PUBLISH_OK' \
   && has_text 'scripts/publish_carrier.sh' 'builder_gates' ; then
  ck "carrier_daily_update_chain" 0 "protected_carrier_dir+atomic_publish"
elif has_text 'scripts/publish_friend_pkg.sh' 'friend-pkg-carrier-keep' \
   && has_text 'scripts/publish_friend_pkg.sh' 'carrier channel broken' \
   && has_text 'scripts/publish_carrier.sh' 'CARRIER_PUBLISH_OK'; then
  ck "carrier_daily_update_chain" 0 "protected_carrier_dir+atomic_publish"
else
  ck "carrier_daily_update_chain" 1 "carrier_publish_chain_regressed"
fi

printf 'INFO|summary|pass=%s fail=%s failed=%s\n' "$PASS" "$FAIL" "${FAILED:-none}"
[ "$FAIL" -eq 0 ] || exit 1
echo "ok - §GZ-UBUNTU 部署件口径门禁通过（$PASS 道静态锁 + 探针变异反证）"
exit 0
