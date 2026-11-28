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
        # §SIDE-POLICY-DEFER 2026-11-27：盒子跳过盘中投递的挂起账本。
        self.mod.SIDE_POLICY_DEFERRED = os.path.join(self.dir, "side_policy_urgent.deferred.json")
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
            self.assertEqual(self.mod.ssh_intraday_trigger("v180-20261124"), (True, ""))
        encoded = run.call_args.args[0][-1]
        self.assertIn("-Intraday", base64.b64decode(encoded).decode("utf-16-le"))
        proc.returncode = 1
        with mock.patch.object(self.mod.os.path, "exists", return_value=True), \
                mock.patch.object(self.mod.subprocess, "run", return_value=proc):
            self.assertEqual(self.mod.ssh_intraday_trigger("v180-20261124"), (False, ""))

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
        self.mod.ssh_intraday_trigger = lambda ver: calls.append(ver) or (False, "")
        self.assertFalse(self.mod.retry_pending_side_policy())
        self.assertEqual(calls, ["v180-20261124"])

    # §SIDE-POLICY-DEFER 2026-11-27 现场：盒子 `intraday_priority=false` ⇒ 盘中投递一律
    # `intraday_skip_no_priority`（rc=0、一个字节都没拉）⇒ ACK 永远不来 ⇒ 重试定时器每
    # 30 秒拉一轮、每轮 2 分钟烘焙，版本号一路涨（当天 v2063）而盒子一次没收到。
    # 跳过必须记成"已发布、等收盘那一次"，重试轮短路；ACK 到达或 latch 变更即解除。
    def _skip_tail(self):
        return "CARRIER_UPDATE_RESULT=OK version=v180-20261124 reason=intraday_skip_no_priority"

    def _defer_req(self, reason="cold_start_open", until="15:00"):
        return {"request_id": "r1", "day": "20261124", "kind": "reversal",
                "until": until, "reason": reason}

    def test_ssh_skip_parks_request_instead_of_retrying_forever(self):
        req = self._defer_req()
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, req)
        self.mod._atomic_json(self.mod.SIDE_POLICY_PENDING, {"r1": "v180-20261124"})
        self.mod.ssh_intraday_trigger = mock.Mock(return_value=(True, self._skip_tail()))
        self.assertTrue(self.mod.retry_pending_side_policy())
        deferred = self.mod._read_json(self.mod.SIDE_POLICY_DEFERRED)
        self.assertEqual(deferred["r1"]["version"], "v180-20261124")
        self.assertEqual(deferred["r1"]["latch"], ["20261124", "reversal", "15:00", "cold_start_open"])
        # 请求已被撤掉（盒子跳过 ⇒ 产物等它自己的班次），pending 也一起清；
        # 不撤的话 systemd 的 PathExists 会在每次退出后立刻重触发，直到启动限流。
        self.assertFalse(os.path.exists(self.mod.SIDE_POLICY_REQ))
        self.assertEqual(self.mod._read_json(self.mod.SIDE_POLICY_PENDING), {})

    # 挂起即短路：加急通道判「该等」时必须立刻返回，既不烘焙也不 ssh ——
    # 留着往下走的表现是「空转几十秒还顺手投了一次盒」。
    def test_deferred_short_circuits_urgent_run_without_bake_or_ssh(self):
        req = self._defer_req()
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, req)
        self.mod._atomic_json(self.mod.SIDE_POLICY_DEFERRED, {
            "r1": {"version": "v180-20261124",
                   "latch": ["20261124", "reversal", "15:00", "cold_start_open"],
                   "reason": "intraday_skip_no_priority"}})
        build = tempfile.mkdtemp()
        os.makedirs(os.path.join(build, "qmt_gateway"))
        self.addCleanup(shutil.rmtree, build, ignore_errors=True)
        self.mod.BUILD_DIR = build
        patches = {"publish_gateway_files": mock.Mock(),
                   "apply_static_params_base": mock.Mock(side_effect=RuntimeError("bake reached"))}
        for name, value in patches.items():
            patcher = mock.patch.object(self.mod, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.mod.ssh_intraday_trigger = mock.Mock()
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            self.assertEqual(self.mod._main_impl(), 0)
        self.assertFalse(patches["apply_static_params_base"].called)
        self.assertFalse(patches["publish_gateway_files"].called)
        self.assertFalse(self.mod.ssh_intraday_trigger.called)
        # 短路同时撤掉被重写的 req（PathExists 是 level-triggered，不撤会反复触发）。
        self.assertFalse(os.path.exists(self.mod.SIDE_POLICY_REQ))

    def test_urgent_without_request_is_idle_and_never_bakes(self):
        """没有加急请求时，`--urgent-side-policy` 必须立即空转退出。

        现场（13:21）：重试定时器每 30 秒拉一次 `--urgent-side-policy`，即使 req 已被
        reaper 清掉也照跑完整烘焙——sha 随买点/利好池漂移，于是每轮 2 分钟烘焙 + 每轮
        投递，盒子客户端被反复重启。加急通道只服务加急请求。"""
        build = tempfile.mkdtemp()
        os.makedirs(os.path.join(build, "qmt_gateway"))
        self.addCleanup(shutil.rmtree, build, ignore_errors=True)
        self.mod.BUILD_DIR = build
        self.assertFalse(os.path.exists(self.mod.SIDE_POLICY_REQ))
        patches = {"publish_gateway_files": mock.Mock(),
                   "apply_static_params_base": mock.Mock(side_effect=RuntimeError("bake reached"))}
        for name, value in patches.items():
            patcher = mock.patch.object(self.mod, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            self.assertEqual(self.mod._main_impl(), 0)
        self.assertFalse(patches["apply_static_params_base"].called)
        self.assertFalse(patches["publish_gateway_files"].called)

    def test_deferred_does_not_cover_a_changed_latch(self):
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, self._defer_req(reason="pm1300_index_drop"))
        self.mod._atomic_json(self.mod.SIDE_POLICY_DEFERRED, {
            "r1": {"version": "v180-20261124",
                   "latch": ["20261124", "reversal", "15:00", "cold_start_open"]}})
        self.assertFalse(self.mod.side_policy_deferred_ok())

    def test_deferred_released_once_the_box_acks_that_version(self):
        self.mod._atomic_json(self.mod.SIDE_POLICY_REQ, self._defer_req())
        self.mod._atomic_json(self.mod.SIDE_POLICY_PENDING, {"r1": "v180-20261124"})
        self.mod._atomic_json(self.mod.SIDE_POLICY_DEFERRED, {
            "r1": {"version": "v180-20261124",
                   "latch": ["20261124", "reversal", "15:00", "cold_start_open"]}})
        with open(self.mod.CARRIER_ACK_HISTORY, "w", encoding="utf-8") as fh:
            fh.write(json.dumps({"version": "v180-20261124", "ok": True}) + "\n")
        self.assertFalse(self.mod.side_policy_deferred_ok())
        self.assertTrue(self.mod.reap_side_policy_requests(self.now))
        self.assertEqual(self.mod._read_json(self.mod.SIDE_POLICY_DEFERRED), {})
        self.assertFalse(os.path.exists(self.mod.SIDE_POLICY_REQ))

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
        self.mod.ssh_intraday_trigger = lambda ver: calls.append(ver) or (True, "")
        with mock.patch.object(sys, "argv", ["hotreload", "--urgent-side-policy"]):
            self.assertEqual(self.mod._main_impl(), 0)
        self.assertEqual(calls, [version])
        self.assertFalse(patches["apply_static_params_base"].called)
        pending = self.mod._read_json(self.mod.SIDE_POLICY_PENDING)
        self.assertEqual(pending, {"r1": version})

    def test_real_sha_early_branch_rebakes_when_manifest_lost_latch(self):
        self._early_fixture(latch=False)
        self.mod.ssh_intraday_trigger = mock.Mock(return_value=(True, ""))
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
        self.mod.ssh_intraday_trigger = lambda ver: calls.append(ver) or (True, "")
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
        self.mod.ssh_intraday_trigger = mock.Mock(return_value=(True, ""))
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
        # 2026-11-27：由 glob 收窄成 OWNER_UID 一条（别的账号的 req 本链永不消费，
        # glob 会让 .path 每 0.06 秒重触发直到 unit-start-limit-hit）。uid 与 .service
        # 的 Environment=OWNER_UID= 必须逐字相同，见下面的等值锁。
        self.assertIn("ConditionPathExists=/var/lib/quant-trading-v2/accounts/u_1785377969825355000/side_policy_urgent.req", service)
        path_unit = open(os.path.join(base, "quant-carrier-side-policy.path"), encoding="utf-8").read()
        self.assertIn("PathExists=/var/lib/quant-trading-v2/accounts/u_1785377969825355000/side_policy_urgent.req", path_unit)
        self.assertNotIn("PathExistsGlob=", path_unit)
        self.assertNotIn("ConditionPathExistsGlob=", service)
        # 系统日历的秒级 /30 只重复当前分钟；小时通配才是真正的每 30 秒重试窗。
        timer = open(os.path.join(base, "quant-carrier-side-policy-retry.timer"), encoding="utf-8").read()
        for hour in ("13", "14", "15"):
            self.assertIn("OnCalendar=Mon..Fri *-*-* %s:*:00/30" % hour, timer)
        self.assertNotIn("13:00:00/30", timer)


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

    def test_urgent_unit_watches_only_the_owner_account(self):
        """systemd 的路径条件必须与 .service 的 OWNER_UID 同源，且**不许**用 glob。

        为什么从 glob 收窄（2026-11-27 现场）：`carrier_manifest` 是按账号读写的
        （`internal/server/carrier.go`），本链只发布 OWNER 那一个账号；别的账号
        （现场是 facai2026）写出自己的 req 后永远没人消费 ⇒ `PathExistsGlob` 一直成立
        ⇒ 每 0.06 秒重触发一次 service，10 分钟内就把 `.path` 打成 unit-start-limit-hit。
        负锁两枚：glob 回潮、路径退回根目录，任一命中即红。
        """
        deploy = os.path.join(REPO, "deploy", "gz-ubuntu")
        with open(os.path.join(deploy, "quant-carrier-side-policy.path"), encoding="utf-8") as fh:
            path_unit = fh.read()
        with open(os.path.join(deploy, "quant-carrier-side-policy.service"), encoding="utf-8") as fh:
            svc_unit = fh.read()
        import re
        owner = re.search(r"^Environment=OWNER_UID=(\S+)$", svc_unit, re.M)
        self.assertIsNotNone(owner, "service 缺 Environment=OWNER_UID=")
        uid = owner.group(1)
        want = "/var/lib/quant-trading-v2/accounts/%s/side_policy_urgent.req" % uid
        self.assertIn("PathExists=%s" % want, path_unit)
        self.assertIn("ConditionPathExists=%s" % want, svc_unit)
        # 负锁：glob 与根目录路径都不许回潮。
        for unit in (path_unit, svc_unit):
            self.assertNotIn("side_policy_urgent.req\nPathExistsGlob", unit)
            self.assertNotIn("PathExistsGlob=", unit)
            self.assertNotIn("/var/lib/quant-trading-v2/side_policy_urgent.req", unit)


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

    # 反转加急：没粘 latch 也要能发 bull req（否则数据齐了但盘面翻转永远推不出去）。
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


if __name__ == "__main__":
    unittest.main()
