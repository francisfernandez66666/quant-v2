# test_hotreload_side_policy.py — §SIDE-POLICY 加急烘焙、latch、SSH 重试与 applied reaper。
import base64
import datetime
import importlib.util
import json
import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
PATH = os.path.join(REPO, "scripts", "carrier_hot_reload_gz.py")


def load_module():
    spec = importlib.util.spec_from_file_location("hotreload_side_policy_under_test", PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class HotReloadSidePolicyTest(unittest.TestCase):
    def setUp(self):
        self.mod = load_module()
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir, ignore_errors=True)
        self.mod.DATA_DIR = self.dir
        self.mod.CONFIG_PATH = os.path.join(self.dir, "config.json")
        self.mod.SIDE_POLICY_LATEST = os.path.join(self.dir, "side_policy_latest.json")
        self.mod.SIDE_POLICY_REQ = os.path.join(self.dir, "side_policy_urgent.req")
        self.mod.SIDE_POLICY_PENDING = os.path.join(self.dir, "side_policy_urgent.pending.json")
        self.mod.SIDE_POLICY_ACK = os.path.join(self.dir, "side_policy_urgent.ack")
        self.mod.CARRIER_ACK_HISTORY = os.path.join(self.dir, "carrier_ack_history.jsonl")
        self.now = datetime.datetime(2026, 11, 24, 13, 0, tzinfo=datetime.timezone(datetime.timedelta(hours=8)))

    def _rules_sizing(self):
        path = os.path.join(REPO, "qmt_gateway", "rules_sizing.py")
        spec = importlib.util.spec_from_file_location("rules_sizing_under_test", path)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        return mod

    def snap(self, **over):
        base = {"day": "20261124", "enabled": True, "mode": "bear", "level": 1,
                "allow_new": False, "allow_add": False, "target_multiplier": 0.85,
                "left_weight": 1.0, "right_weight": 0.8, "latch": {},
                "asof_ts": self.now.timestamp(), "freshness_epoch": 1}
        base.update(over)
        return base

    def test_runtime_switch_default_and_explicit(self):
        self.assertFalse(self.mod.runtime_side_policy_enabled())
        self.mod._atomic_json(self.mod.CONFIG_PATH, {"rules": {"runtime": {"side_policy_enabled": True}}})
        self.assertTrue(self.mod.runtime_side_policy_enabled())

    def test_side_policy_is_runtime_param(self):
        self.assertIn("side_policy", self.mod.RUNTIME_PARAM_KEYS)

    def test_digest_ignores_raw_readings_and_asof(self):
        base = self.snap()
        moved = dict(base, asof_ts=999, market_delta=-0.9, slope_delta=-0.4)
        self.assertEqual(self.mod.side_policy_digest(base), self.mod.side_policy_digest(moved))
        changed = dict(base, freshness_epoch=2)
        self.assertNotEqual(self.mod.side_policy_digest(base), self.mod.side_policy_digest(changed))
        changed = dict(base, reason="bear_tighten")
        self.assertNotEqual(self.mod.side_policy_digest(base), self.mod.side_policy_digest(changed))

    def test_urgent_request_sticks_latch_until_1400(self):
        req = {"request_id": "r1", "day": "20261124", "kind": "no_new", "until": "14:00",
               "reason": "pm1300_index_drop"}
        merged = self.mod.merge_side_policy({}, self.snap(allow_new=True), req, self.now)
        self.assertFalse(merged["allow_new"])
        self.assertEqual(merged["latch"]["reason"], "pm1300_index_drop")
        self.assertGreater(merged["latch_until_ts"], self.now.timestamp())

    def test_regular_bake_cannot_clear_active_latch(self):
        current = self.snap(latch={"kind": "no_new", "until": "14:00", "reason": "pm1300_flow_drop"},
                            latch_until_ts=self.now.replace(hour=14).timestamp())
        merged = self.mod.merge_side_policy(current, self.snap(allow_new=True, latch={}), {}, self.now)
        self.assertFalse(merged["allow_new"])
        self.assertEqual(merged["latch"]["reason"], "pm1300_flow_drop")

    def test_missing_latest_fails_closed_with_complete_state(self):
        """D6-c 2026-11-25：读不到当日快照 = 「看不见」，不是「策略是空的」。

        旧行为是把 current（上一次的旧态）当基准继续用，于是页面上的 `{'enabled': True}`
        与「真的有熊市快照」长得一模一样。现在必须 fail-closed：禁 new、禁 add，
        并且 reason 点名是缺读数。

        形状要求（这条是重点）：**必须是完整合法态**。只给 day/allow_new/allow_add
        的半份 dict 会落进 rules_sizing._side_fail_closed()，而它的 allow_add 是 True
        （L1 语义）⇒「读不到」会反手变成「可以加仓」，reason 还会被改写成 bad_value。
        """
        current = self.snap(allow_new=True, target_multiplier=0.7)
        merged = self.mod.merge_side_policy(current, {}, {}, self.now)
        self.assertEqual(merged["mode"], "bear")
        self.assertEqual(merged["level"], 1)
        self.assertFalse(merged["allow_new"])
        self.assertFalse(merged["allow_add"])
        self.assertEqual(merged["target_multiplier"], 1.0)
        self.assertEqual(merged["reason"], "side_policy_state_missing")
        self.assertEqual(merged["day"], self.now.strftime("%Y%m%d"))
        # 完整合法态的七枚必需键必须在场（少了就落进 _side_fail_closed）。
        for key in ("enabled", "day", "mode", "level", "allow_new", "allow_add",
                    "target_multiplier", "asof_ts", "latch_until_ts"):
            self.assertIn(key, merged)
        # 且必须真的被载体规则层认成"限制态"而不是"坏读数"。
        rules_sizing = self._rules_sizing()
        params = {"side_policy_enabled": True, "side_policy": merged,
                  "side_policy_ttl_sec": 2700.0}
        ctx = {"day": self.now.strftime("%Y%m%d"), "now_ts": self.now.timestamp()}
        st = rules_sizing.side_policy_state(params, ctx)
        self.assertTrue(st["enabled"])
        self.assertFalse(st["allow_new"])
        self.assertFalse(st["allow_add"])
        self.assertEqual(st["reason"], "side_policy_state_missing")

    def test_request_retained_until_matching_applied_ack(self):
        req = {"request_id": "r1", "day": "20261124", "kind": "no_new"}
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, req)
        self.mod._atomic_json(self.mod.SIDE_POLICY_PENDING, {"r1": "v180-20261124"})
        self.assertFalse(self.mod.reap_side_policy_requests(self.now))
        self.assertTrue(os.path.exists(self.mod.SIDE_POLICY_REQ))
        with open(self.mod.CARRIER_ACK_HISTORY, "w", encoding="utf-8") as fh:
            fh.write(json.dumps({"version": "v179-20261124", "ok": True}) + "\n")
        self.assertFalse(self.mod.reap_side_policy_requests(self.now))
        with open(self.mod.CARRIER_ACK_HISTORY, "a", encoding="utf-8") as fh:
            fh.write(json.dumps({"version": "v180-20261124", "ok": True}) + "\n")
        self.assertTrue(self.mod.reap_side_policy_requests(self.now))
        self.assertFalse(os.path.exists(self.mod.SIDE_POLICY_REQ))
        self.assertTrue(os.path.exists(self.mod.SIDE_POLICY_ACK))

    def test_ssh_trigger_invokes_intraday_and_maps_rc(self):
        proc = mock.Mock(returncode=0, stdout="", stderr="")
        with mock.patch.object(self.mod.os.path, "exists", return_value=True), \
                mock.patch.object(self.mod.subprocess, "run", return_value=proc) as run:
            self.assertTrue(self.mod.ssh_intraday_trigger("v180-20261124"))
        encoded = run.call_args.args[0][-1]
        self.assertIn("-Intraday", base64.b64decode(encoded).decode("utf-16-le"))
        proc.returncode = 1
        with mock.patch.object(self.mod.os.path, "exists", return_value=True), \
                mock.patch.object(self.mod.subprocess, "run", return_value=proc):
            self.assertFalse(self.mod.ssh_intraday_trigger("v180-20261124"))

    def test_atomic_latest_write_failure_preserves_previous_file(self):
        self.mod._atomic_json(self.mod.SIDE_POLICY_LATEST, self.snap())
        before = open(self.mod.SIDE_POLICY_LATEST).read()
        with mock.patch.object(self.mod.os, "replace", side_effect=OSError("readonly")):
            with self.assertRaises(OSError):
                self.mod._atomic_json(self.mod.SIDE_POLICY_LATEST, self.snap(mode="bull"))
        self.assertEqual(open(self.mod.SIDE_POLICY_LATEST).read(), before)

    def test_pending_version_retries_ssh_without_bump(self):
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, {"request_id": "r1"})
        self.mod._atomic_json(self.mod.SIDE_POLICY_PENDING, {"r1": "v180-20261124"})
        calls = []
        self.mod.ssh_intraday_trigger = lambda ver: calls.append(ver) or False
        self.assertFalse(self.mod.retry_pending_side_policy())
        self.assertEqual(calls, ["v180-20261124"])

    # _early_fixture 构造 SHA 相同的早退夹具，区分待发 latch 与已发布载荷。
    def _early_fixture(self, latch=True, pending_version="v179-20261124"):
        build = tempfile.mkdtemp()
        os.makedirs(os.path.join(build, "qmt_gateway"))
        self.mod.BUILD_DIR = build
        self.mod.CARRIER_DIR = tempfile.mkdtemp()
        self.mod.STATE = os.path.join(self.dir, "hotreload.sha")
        self.mod.BUY_STATE_PATH = os.path.join(build, "qmt_gateway", "buy_signals_state.json")
        today = datetime.datetime.now().astimezone().strftime("%Y%m%d")
        req = {"request_id": "r1", "day": today, "kind": "no_new", "until": "23:59",
               "reason": "pm1300_index_drop"}
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, req)
        self.mod._atomic_json(self.mod.SIDE_POLICY_PENDING, {"r1": pending_version})
        self.mod._atomic_json(self.mod.CONFIG_PATH, {"rules": {"runtime": {"side_policy_enabled": True}}})
        snap = self.snap(day=today, allow_new=False, reason="pm1300_latch",
                         latch={"kind": "no_new", "until": "23:59" if latch else "14:01",
                                "reason": req["reason"]})
        params = {"params": {"side_policy": snap, "side_policy_enabled": True}}
        self.mod._atomic_json(os.path.join(build, "qmt_gateway", "local_executor_params.json"), params)
        self.mod._atomic_json(self.mod.SIDE_POLICY_LATEST, snap)
        version = "v180-20261124"
        account = os.path.join(self.dir, "accounts", self.mod.OWNER_UID)
        os.makedirs(account)
        self.mod._atomic_json(os.path.join(account, "carrier_manifest.json"),
                              {"strategy_version": version})
        with open(os.path.join(self.mod.CARRIER_DIR, "qmt_internal_strategy_%s.py" % version), "w") as fh:
            fh.write("PARAMS = %r\n" % params["params"])
        # 对齐真实烘焙前 SHA 载荷，确保测试进入早退分支；候选含请求，发布件可能不含。
        # 中文说明：候选状态含 latch，旧发布载荷未必含同一 latch。
        merged = self.mod.merge_side_policy(snap, snap, req, datetime.datetime.now().astimezone())
        merged["enabled"] = True
        import hashlib
        payload = {"wl": ["dragon"], "rules": [], "bearish": [], "bullish": {},
                   "force_sell": [], "reversal": hashlib.sha256(b"[]").hexdigest()[:16],
                   "signal_pool": [], "precalc": {}, "sector_map": {}, "buy_codes": [],
                   "t_bake": hashlib.sha256(json.dumps({"a": {}, "m": {}, "c": self.mod.T_SIGNAL_CODES},
                       ensure_ascii=True, sort_keys=True).encode()).hexdigest()[:16],
                   "tp_plan": self.mod.tp_plan_digest({}),
                   "flow_snapshot": self.mod.flow_snapshot_digest({}),
                   "side_policy": self.mod.side_policy_digest(merged), "side_policy_enabled": True,
                   "static_base": "base", "buckets": {"industry": 0, "concept": 0,
                       "digest": hashlib.sha256(json.dumps({"i": [], "c": []}, ensure_ascii=True).encode()).hexdigest()[:16]}}
        with open(self.mod.STATE, "w") as fh:
            fh.write(hashlib.sha256(json.dumps(payload, ensure_ascii=True).encode()).hexdigest())
        patches = {
            "publish_gateway_files": mock.Mock(), "load_live": mock.Mock(return_value=({"qmt": {"strategies": ["dragon"]}}, [])),
            "extract_news_gate": mock.Mock(return_value=None), "extract_bearish": mock.Mock(return_value=[]),
            "extract_bullish": mock.Mock(return_value={}), "extract_signal_codes": mock.Mock(return_value=[]),
            "load_precalc": mock.Mock(return_value=({}, {})), "load_sector_map": mock.Mock(return_value=({}, {})),
            "bake_t_anchor_book": mock.Mock(return_value={}), "bake_t_macro": mock.Mock(return_value={}),
            "bake_flow_snapshot": mock.Mock(return_value=({}, False)), "bake_tp_plan": mock.Mock(return_value={}),
            "static_base_digest": mock.Mock(return_value="base"), "extract_buy_codes": mock.Mock(return_value=[]),
            "apply_static_params_base": mock.Mock(side_effect=RuntimeError("bake reached")),
        }
        for name, value in patches.items():
            patcher = mock.patch.object(self.mod, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        return req, version, patches

    def test_real_sha_early_branch_rebinds_manifest_without_bump(self):
        req, version, patches = self._early_fixture()
        calls = []
        self.mod.ssh_intraday_trigger = lambda ver: calls.append(ver) or True
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            self.assertEqual(self.mod._main_impl(), 0)
        self.assertEqual(calls, [version])
        self.assertFalse(patches["apply_static_params_base"].called)
        pending = self.mod._read_json(self.mod.SIDE_POLICY_PENDING)
        self.assertEqual(pending, {"r1": version})

    def test_real_sha_early_branch_rebakes_when_manifest_lost_latch(self):
        self._early_fixture(latch=False)
        self.mod.ssh_intraday_trigger = mock.Mock(return_value=True)
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            with self.assertRaisesRegex(RuntimeError, "bake reached"):
                self.mod._main_impl()
        self.assertTrue(os.path.exists(self.mod.SIDE_POLICY_REQ))

    def test_manifest_without_version_is_nonzero_and_keeps_req(self):
        self._early_fixture()
        account = os.path.join(self.dir, "accounts", self.mod.OWNER_UID)
        self.mod._atomic_json(os.path.join(account, "carrier_manifest.json"), {})
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            self.assertEqual(self.mod._main_impl(), 3)
        self.assertTrue(os.path.exists(self.mod.SIDE_POLICY_REQ))

    def test_expired_request_remove_failure_is_nonzero_and_keeps_req(self):
        req, _version, _patches = self._early_fixture()
        req["day"] = "20000101"
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, req)
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            with mock.patch.object(self.mod.os, "remove", side_effect=OSError("readonly")):
                with self.assertRaises(OSError):
                    self.mod._main_impl()
        self.assertTrue(os.path.exists(self.mod.SIDE_POLICY_REQ))

    def test_pending_write_failure_keeps_req_and_does_not_ssh(self):
        self._early_fixture()
        calls = []
        self.mod.ssh_intraday_trigger = lambda ver: calls.append(ver) or True
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            with mock.patch.object(self.mod, "_atomic_json", side_effect=OSError("readonly")):
                with self.assertRaises(OSError):
                    self.mod._main_impl()
        self.assertEqual(calls, [])
        self.assertTrue(os.path.exists(self.mod.SIDE_POLICY_REQ))

    def test_ack_before_rebind_reaps_without_ssh(self):
        _req, version, _patches = self._early_fixture()
        with open(self.mod.CARRIER_ACK_HISTORY, "w") as fh:
            fh.write(json.dumps({"version": version, "ok": True}) + "\n")
        self.mod.ssh_intraday_trigger = mock.Mock(return_value=True)
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            self.assertEqual(self.mod._main_impl(), 0)
        self.assertFalse(self.mod.ssh_intraday_trigger.called)
        self.assertFalse(os.path.exists(self.mod.SIDE_POLICY_REQ))

    def test_out_of_order_old_ack_does_not_reap_new_pending(self):
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, {"request_id": "r1"})
        self.mod._atomic_json(self.mod.SIDE_POLICY_PENDING, {"r1": "v180-20261124"})
        with open(self.mod.CARRIER_ACK_HISTORY, "w") as fh:
            fh.write(json.dumps({"version": "v178-20261124", "ok": True}) + "\n")
            fh.write(json.dumps({"version": "v179-20261124", "ok": False}) + "\n")
        self.mod._atomic_json(self.mod.SIDE_POLICY_PENDING, {"r1": "v179-20261124"})
        self.assertFalse(self.mod.reap_side_policy_requests(self.now))
        self.assertTrue(os.path.exists(self.mod.SIDE_POLICY_REQ))

    def test_ssh_exception_keeps_req_and_pending(self):
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, {"request_id": "r1"})
        self.mod._atomic_json(self.mod.SIDE_POLICY_PENDING, {"r1": "v180-20261124"})
        self.mod.ssh_intraday_trigger = mock.Mock(side_effect=OSError("ssh down"))
        with self.assertRaises(OSError):
            self.mod.retry_pending_side_policy()
        self.assertTrue(os.path.exists(self.mod.SIDE_POLICY_REQ))
        self.assertEqual(self.mod._read_json(self.mod.SIDE_POLICY_PENDING), {"r1": "v180-20261124"})

    def test_flock_busy_returns_75_without_entering_bake(self):
        self.mod.HOTRELOAD_LOCK = os.path.join(self.dir, "lock")
        with open(self.mod.HOTRELOAD_LOCK, "w") as lock:
            import fcntl
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with mock.patch.object(self.mod, "_main_impl") as impl:
                self.assertEqual(self.mod.main(), 75)
                self.assertFalse(impl.called)

    def test_urgent_skips_pregen_but_keeps_flow_bake_once(self):
        self._early_fixture(latch=False)
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            with mock.patch.object(self.mod.subprocess, "run") as run:
                with self.assertRaisesRegex(RuntimeError, "bake reached"):
                    self.mod._main_impl()
        self.assertFalse(run.called)

    def test_regular_pregen_throttle_and_sector_30min_unchanged(self):
        self._early_fixture(latch=False)
        os.makedirs(os.path.join(self.mod.BUILD_DIR, "scripts"))
        self.mod._atomic_json(os.path.join(self.mod.BUILD_DIR, "qmt_gateway", "precalc_status.json"),
                              {"day": "20000101", "coverage": 0})
        with mock.patch.object(sys, "argv", ["hotreload"]):
            with mock.patch.object(self.mod.subprocess, "run", return_value=mock.Mock(returncode=0, stdout="", stderr="")) as run:
                with self.assertRaisesRegex(RuntimeError, "bake reached"):
                    self.mod._main_impl()
        self.assertEqual([c.args[0][1] for c in run.call_args_list],
                         ["scripts/pregen_carrier_factors_gz.py", "scripts/pregen_sector_map_gz.py"])
        self.assertEqual(run.call_args_list[1].kwargs["timeout"], 600)

    def test_urgent_units_exist(self):
        base = os.path.join(REPO, "deploy", "gz-ubuntu")
        for name in ("quant-carrier-side-policy.path", "quant-carrier-side-policy.service",
                     "quant-carrier-side-policy-retry.timer"):
            self.assertTrue(os.path.isfile(os.path.join(base, name)), name)
        service = open(os.path.join(base, "quant-carrier-side-policy.service"), encoding="utf-8").read()
        self.assertIn("--urgent-side-policy", service)
        self.assertIn("Restart=on-failure", service)
        # D6-c 2026-11-25：引擎按账号落盘 ⇒ 条件与监视都改成 glob（不许再硬编码 uid）。
        self.assertIn("ConditionPathExistsGlob=/var/lib/quant-trading-v2/accounts/*/side_policy_urgent.req", service)
        path_unit = open(os.path.join(base, "quant-carrier-side-policy.path"), encoding="utf-8").read()
        self.assertIn("PathExistsGlob=/var/lib/quant-trading-v2/accounts/*/side_policy_urgent.req", path_unit)
        self.assertNotIn("PathExists=/var/lib/quant-trading-v2/side_policy_urgent.req", path_unit)
        # 系统日历的秒级 /30 只重复当前分钟；小时通配才是真正的每 30 秒重试窗。
        timer = open(os.path.join(base, "quant-carrier-side-policy-retry.timer"), encoding="utf-8").read()
        for hour in ("13", "14", "15"):
            self.assertIn("OnCalendar=Mon..Fri *-*-* %s:*:00/30" % hour, timer)
        self.assertNotIn("13:00:00/30", timer)


if __name__ == "__main__":
    unittest.main()

    # ------------------------------------------------------------------
    # D6-c 2026-11-25 第二半：发版后写点搬到 accounts/<uid>/，读面必须跟着走
    # ------------------------------------------------------------------
    def test_state_path_prefers_the_account_dir_and_falls_back(self):
        """三态：账号目录有 ⇒ 用账号那份；只在根目录 ⇒ 用根那份；都没有 ⇒ 用账号路径（写点）。

        为什么"都没有"要返回账号路径而不是根路径：这个函数的返回值有三处是**写入**用途
        （.req 的创建、pending 的落盘、ack 的清理）。返回根路径等于在迁移后又往旧位置写，
        于是一次发版能造出两份互不相认的状态文件。
        """
        acct = os.path.join(self.dir, "accounts", "u_owner")
        os.makedirs(acct, exist_ok=True)
        self.mod._SIDE_ACCT_DIR = acct
        self.mod.DATA_DIR = self.dir

        # ① 都没有 ⇒ 用账号路径（写点）。
        self.assertEqual(self.mod._side_state_path("side_policy_urgent.req"),
                         os.path.join(acct, "side_policy_urgent.req"))
        # ② 只有根目录有 ⇒ 读根那份（跨版本过渡期不能把旧状态当"没有"）。
        with open(os.path.join(self.dir, "side_policy_latest.json"), "w", encoding="utf-8") as fh:
            fh.write("{}")
        self.assertEqual(self.mod._side_state_path("side_policy_latest.json"),
                         os.path.join(self.dir, "side_policy_latest.json"))
        # ③ 账号目录也有 ⇒ 账号那份赢（新写点优先）。
        with open(os.path.join(acct, "side_policy_latest.json"), "w", encoding="utf-8") as fh:
            fh.write("{}")
        self.assertEqual(self.mod._side_state_path("side_policy_latest.json"),
                         os.path.join(acct, "side_policy_latest.json"))

    def test_urgent_unit_watches_the_account_dir_glob(self):
        """systemd 的路径条件必须用 glob：账号 UID 不是常量，写死一个 UID 会在换账号后静默失效。

        `PathExistsGlob` 是 .path 的触发判据、`ConditionPathExistsGlob` 是 .service 的启动条件，
        两者少一个就变成"req 一直堆着没人烤"（加急发布最需要它的时候失效）。
        """
        deploy = os.path.join(REPO, "deploy", "gz-ubuntu")
        with open(os.path.join(deploy, "quant-carrier-side-policy.path"), encoding="utf-8") as fh:
            path_unit = fh.read()
        with open(os.path.join(deploy, "quant-carrier-side-policy.service"), encoding="utf-8") as fh:
            svc_unit = fh.read()
        glob = "accounts/*/side_policy_urgent.req"
        self.assertIn("PathExistsGlob=/var/lib/quant-trading-v2/%s" % glob, path_unit)
        self.assertIn("ConditionPathExistsGlob=/var/lib/quant-trading-v2/%s" % glob, svc_unit)
        # 负锁：写死单个 UID 或退回根目录都算回潮。
        for unit in (path_unit, svc_unit):
            self.assertNotIn("/var/lib/quant-trading-v2/side_policy_urgent.req", unit)
            self.assertNotIn("/accounts/u_1785377969825355000/", unit)


class SideReversalReqTest(unittest.TestCase):
    """2026-11-25：牛熊反转走同一条 urgent req 链，但**不许**粘 13:00 的 no_new latch。

    开盘默认 bear 只是数据未齐的临时剧本；数据齐了翻成 bull 时若还带 latch，
    页面显示"允许"而盒子上新仓全被挡，等于反转白烘。
    """

    def setUp(self):
        self.mod = load_module()
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir, ignore_errors=True)
        self.mod.SIDE_POLICY_LATEST = os.path.join(self.dir, "side_policy_latest.json")

    def test_reversal_req_publishes_bull_without_latch(self):
        now = datetime.datetime(2026, 11, 25, 10, 15,
                                tzinfo=datetime.timezone(datetime.timedelta(hours=8)))
        day = "20261125"
        current = {"day": day, "enabled": True, "mode": "bear", "level": 1,
                   "allow_new": False, "allow_add": True, "target_multiplier": 1.0,
                   "left_weight": 1.0, "right_weight": 1.0, "reason": "side_policy_cold_start_bear",
                   "asof_ts": now.timestamp(), "latch_until_ts": 0}
        latest = dict(current, mode="bull", level=1, allow_new=True,
                      target_multiplier=1.2, reason="bull_mirror")
        req = {"request_id": "%s-reversal-reversal_bear_to_bull" % day, "day": day,
               "kind": "reversal", "until": "15:00", "reason": "reversal_bear_to_bull"}
        merged = self.mod.merge_side_policy(current, latest, req, now)
        self.assertEqual(merged["mode"], "bull")
        self.assertTrue(merged["allow_new"])
        latch = merged.get("latch") or {}
        self.assertNotEqual(latch.get("kind"), "no_new")

    def test_pm_no_new_req_still_latches(self):
        now = datetime.datetime(2026, 11, 25, 13, 1,
                                tzinfo=datetime.timezone(datetime.timedelta(hours=8)))
        day = "20261125"
        base = {"day": day, "enabled": True, "mode": "bull", "level": 1,
                "allow_new": True, "allow_add": True, "target_multiplier": 1.0,
                "left_weight": 1.0, "right_weight": 1.0, "reason": "bull_mirror",
                "asof_ts": now.timestamp(), "latch_until_ts": 0}
        req = {"request_id": "%s-no_new-pm1300_both" % day, "day": day,
               "kind": "no_new", "until": "14:00", "reason": "pm1300_both"}
        merged = self.mod.merge_side_policy(base, base, req, now)
        self.assertEqual((merged.get("latch") or {}).get("kind"), "no_new")
        self.assertFalse(merged["allow_new"])
