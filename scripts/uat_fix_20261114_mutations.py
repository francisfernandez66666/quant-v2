#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§20261114 变异反证总表（F2/F3/F5/F6/F7/F8；F1 在 cloud_byte_uat_readonly_test.mjs）。

用法：python3 scripts/uat_fix_20261114_mutations.py <段号|all>

每枚反证做三件事（防止"恒绿假锁"）：
  ① 基线文本必须满足该段的判据（判据与 verify_changes.sh 对应段逐字同源）；
  ② 在**镜像副本**（/tmp，不碰仓库）上做一次精确替换，落地次数必须恰好 1；
  ③ 同一判据作用在镜像文本上必须转红。
输出 MUT_<段号>_<编号>=RED；任一环节不满足即 exit 1 并打印原因。
English: mirror-based mutation counter-proofs for the 2026-11-14 UAT fix gates.
"""
import pathlib
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent

# (编号, 相对文件, 旧串, 新串, 判据类型, 判据参数)
# 判据类型：
#   has(串)          —— 串必须出现（删确认位/删接线后必红）
#   absent(串)       —— 串必须不出现（把旧文案加回来必红）
#   order(前,后)     —— 前者必须排在后者之前（bcrypt 顺序）
#   count(串,数)     —— 串出现次数必须等于给定值
MUTATIONS = {
    "184": [
        ("F1-1", "internal/server/paper.go", 'req.Confirm != "RESET_PAPER"', '', "has", 'req.Confirm != "RESET_PAPER"'),
        ("F1-2", "internal/server/admin.go", "if req.DryRun == nil {", "", "has", "if req.DryRun == nil {"),
        ("F1-3", "internal/server/handlers_fix.go", "AvailableBalance *float64", "AvailableBalance float64", "has", "AvailableBalance *float64"),
        ("F1-4", "internal/server/decode_strict.go", "func withIgnoredKeys(", "", "has", "func withIgnoredKeys("),
        ("F1-5", "internal/server/handlers_fix.go", 'CLEAR_ALERTS"', 'CLEAR_NOTHING"', "has", 'CLEAR_ALERTS"'),
        ("F1-6", "internal/server/handlers_fix.go", "\tctrl := s.liveCtrlFor(uid)\n\tif ctrl == nil {", '\tctrl := s.ctrlFor("u-fixed")\n\tif ctrl == nil {', "has", "ctrl := s.liveCtrlFor(uid)"),
        # §20261115 补缺：backtest-toggle 的两种回归形态各锁一枚（本地栈写探针锤出）。
        ("F1-7", "internal/server/library.go", 'Enabled *bool `json:"enabled"`', 'Enabled bool `json:"enabled"`', "has", 'Enabled *bool `json:"enabled"`'),
        # 锚必须带上下文：library.go 里 decodeStrictB(w, r, &body) 出现 2 次，
        # 只换短串会「落地 2 次」且判据仍绿（另一处还在）。用 *bool 字段绑定唯一位置。
        ("F1-8", "internal/server/library.go",
         '\t\tEnabled *bool `json:"enabled"`\n\t}\n\tif _, ok := decodeStrictB(w, r, &body); !ok {',
         '\t\tEnabled *bool `json:"enabled"`\n\t}\n\tif err := json.NewDecoder(r.Body).Decode(&body); err != nil {',
         "has",
         '\t\tEnabled *bool `json:"enabled"`\n\t}\n\tif _, ok := decodeStrictB(w, r, &body); !ok {'),
    ],
    "185": [
        ("C2", "deploy/gz-ubuntu/Caddyfile", "\thandle /readyz {\n\t\treverse_proxy 127.0.0.1:8081\n\t}\n", "", "has", "handle /readyz {"),
    ],
    "186": [
        ("C1", "internal/store/real_positions.go", "-- 2026-11-13 owner 裁决改为", "-- (mutated)", "count", ("-- 2026-11-13 owner 裁决改为", 2), "all"),
        ("C2", "internal/store/real_positions.go", "-- signal_id 有意不出现在 SET 列表", "-- 永久不覆盖（mutation）", "absent", "永久不覆盖"),
    ],
    "187": [
        ("M1", "internal/auth/auth.go", '\t\t\tif err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {\n\t\t\t\treturn nil, fmt.Errorf("wrong password")\n\t\t\t}\n\t\t\tif !u.Enabled {\n\t\t\t\treturn nil, fmt.Errorf("account disabled")\n\t\t\t}\n\t\t\tif u.expired() {\n\t\t\t\treturn nil, fmt.Errorf("account expired")\n\t\t\t}', '\t\t\tif !u.Enabled {\n\t\t\t\treturn nil, fmt.Errorf("account disabled")\n\t\t\t}\n\t\t\tif u.expired() {\n\t\t\t\treturn nil, fmt.Errorf("account expired")\n\t\t\t}\n\t\t\tif err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {\n\t\t\t\treturn nil, fmt.Errorf("wrong password")\n\t\t\t}', "order", ("bcrypt.CompareHashAndPassword", "u.expired()")),
        ("M2", "internal/server/server.go", "func loginFailureCode(err error) (code, message string) {", "func loginFailureCodeDisabled(err error) (code, message string) {", "has", "loginFailureCode(err error) (code, message string) {"),
    ],
    "188": [
        ("C1", "web/src/api/index.js", "timeout: REVIEW_TIMEOUT_MS", "", "has", "timeout: REVIEW_TIMEOUT_MS"),
    ],
    "189": [
        ("C1", "internal/server/handlers_qmt_admin.go", 'writeJSON(w, http.StatusConflict, map[string]string{', 'writeError(w, 502, "gateway_not_configured"', "count", ("http.StatusConflict", 3), "all"),
    ],
}


def criterion(kind, arg, text):
    if kind == "has":
        return arg in text
    if kind == "absent":
        return arg not in text
    if kind == "order":
        before, after = arg
        return text.find(before) != -1 and text.find(after) != -1 and text.find(before) < text.find(after)
    if kind == "count":
        needle, want = arg
        return text.count(needle) == want
    raise ValueError("unknown criterion: %s" % kind)


def run_section(sid):
    rows = MUTATIONS[sid]
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="mut%s-" % sid))
    for num, rel, old, new, kind, arg, *extra in rows:
        src = (ROOT / rel).read_text(encoding="utf-8")
        assert criterion(kind, arg, src), "§%s %s 基线判据本应为绿（判据写歪了）" % (sid, num)
        n = src.count(old)
        if extra and extra[0] == "all":
            assert n >= 2, "§%s %s all 模式锚落地 %d 次（应 ≥2）" % (sid, num, n)
            mutated = src.replace(old, new)
        else:
            assert n == 1, "§%s %s 变异锚落地 %d 次（应为 1，锚漂了）" % (sid, num, n)
            mutated = src.replace(old, new, 1)
        mirror = tmp / ("%s_%s" % (sid, num))
        mirror.write_text(mutated, encoding="utf-8")
        text = mirror.read_text(encoding="utf-8")
        assert not criterion(kind, arg, text), "§%s %s 变异后判据仍绿＝锁没牙" % (sid, num)
        print("MUT_%s_%s=RED" % (sid, num))
    print("MUT_%s_COUNT=%d" % (sid, len(rows)))


def main():
    arg = sys.argv[1] if len(sys.argv) > 1 else "all"
    sids = list(MUTATIONS.keys()) if arg == "all" else [arg]
    for sid in sids:
        if sid not in MUTATIONS:
            print("MUT_UNKNOWN_SECTION=%s" % sid)
            return 1
        run_section(sid)
    print("MUTATIONS_OK sections=%s" % ",".join(sids))
    return 0


if __name__ == "__main__":
    sys.exit(main())
