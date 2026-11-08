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

printf 'INFO|summary|pass=%s fail=%s failed=%s\n' "$PASS" "$FAIL" "${FAILED:-none}"
[ "$FAIL" -eq 0 ] || exit 1
echo "ok - §GZ-UBUNTU 部署件口径门禁通过（$PASS 道静态锁 + 探针变异反证）"
exit 0
