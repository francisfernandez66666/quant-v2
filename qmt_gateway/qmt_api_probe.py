# qmt_api_probe.py -- READ-ONLY API surface probe for the QMT built-in Python strategy.
#
# Owner request 2026-11-10 22:00: find out whether THIS broker's QMT sandbox
# exposes any news / announcement / bulletin data interface that a built-in
# strategy could legally consume (data served by the QMT client itself, with
# no strategy-originated file or network IO).
#
# HARD RULES (this file is deliberately minimal):
#   1. NO open()/read()/write(), NO socket/http/requests/urllib, NO database.
#   2. NO trading calls (passorder/cancel). NOTHING is ever ordered.
#   3. NO calls to the probed functions -- presence is checked with hasattr()
#      / dir() only. A real news API might open a network connection when
#      called; presence-only probing keeps this file read-only by construction.
#   4. Pure ASCII (QMT GBK sandbox), stdlib `sys` only.
#
# POSITIVE CONTROLS: get_full_tick / get_market_data_ex / get_trade_detail_data
# are known-good QMT APIs. If the controls are NOT seen, the probe itself is
# unreliable on this runtime and its negative ("no news API") result must be
# discarded -- never conclude "absent" from a broken detector.
#
# HOW TO RUN (owner, on the physical Win box):
#   1. QMT client -> model research / new Python strategy, paste this whole file.
#   2. Set any one symbol (init() subscribes 000001.SZ, data only) and any bar
#      period; RUN it in PAPER / shadow mode (it places nothing anyway).
#   3. Open the strategy log panel and paste the PROBE_* lines back.
#
# English: presence-only, zero-side-effect introspection of the built-in
# strategy runtime's ContextInfo / globals / loaded modules.

import sys

PROBE_BUILD = "2026-11-10"

# Known-good QMT APIs: proof the detector can actually see functions here.
CONTROL_CONTEXT = ("get_full_tick", "get_market_data_ex", "get_market_data")
CONTROL_GLOBAL = ("get_trade_detail_data", "passorder", "cancel")

# Native QMT data interfaces (documented: quotes, financials, sectors...).
DATA_CONTEXT = (
    "get_financial_data", "get_history_data", "get_stock_list_in_sector",
    "get_sector_list", "get_trading_dates", "get_instrument_detail",
    "get_index_weight", "get_divid_factors", "load_stk_list", "ext_data",
    "get_subscribe_data", "subscribe_quote",
)
DATA_GLOBAL = (
    "get_financial_data", "get_market_data_ex", "get_local_data",
    "get_instrument_detail", "get_sector_list", "get_stock_list_in_sector",
    "get_trading_dates", "get_index_weight", "download_history_data",
    "subscribe_quote", "subscribe_whole_quote",
)

# Candidate NEWS / announcement / bulletin / research interfaces. These are
# guesses across QMT versions; a hit is only a STRING MATCH on an attribute,
# not proof the function is usable, and this probe NEVER calls it.
NEWS_NAMES = (
    "get_news", "get_news_data", "get_news_list", "get_stock_news",
    "get_market_news", "get_flash", "get_flash_news", "get_announcement",
    "get_announcements", "get_notice", "get_notices", "get_bulletin",
    "get_disclosure", "get_report", "get_research_report", "get_reports",
    "get_event", "get_events", "get_information", "get_content",
    "subscribe_news", "subscribe_announcement", "get_news_by_stock",
    "get_announcement_data", "get_public_info",
)

# Extra data-vendor style interfaces (often need network; presence only).
VENDOR_NAMES = (
    "get_xtdata", "xtdata", "xtquant", "get_think_data", "get_wind_data",
    "get_juyuan_data", "get_tushare_data",
)

_done = False


def _names(obj):
    """Return the visible attribute names of obj without calling anything else."""
    try:
        return set(dir(obj))
    except Exception:  # noqa: BLE001 - introspection failure is a probe result
        return set()


def _module_keys():
    """Loaded module names only; never import anything here."""
    try:
        return set(sys.modules.keys())
    except Exception:  # noqa: BLE001
        return set()


def init(ContextInfo):
    """Subscribe one symbol (quote data only). No file, no net, no order."""
    print("PROBE_START build=%s" % PROBE_BUILD)
    try:
        ContextInfo.set_universe(["000001.SZ"])
        print("PROBE_INIT universe=000001.SZ")
    except Exception as exc:  # noqa: BLE001
        print("PROBE_INIT universe_skipped reason=%s" % repr(exc)[:80])


def handlebar(ContextInfo):
    """Probe once, then stay silent forever (no repeated log spam)."""
    global _done
    if _done:
        return
    _done = True

    ctx = _names(ContextInfo)
    glob = set(globals().keys())
    mods = _module_keys()

    ctrl_ctx = [n for n in CONTROL_CONTEXT if n in ctx]
    ctrl_glb = [n for n in CONTROL_GLOBAL if n in glob]

    data_ctx = [n for n in DATA_CONTEXT if n in ctx]
    data_glb = [n for n in DATA_GLOBAL if n in glob]

    news_ctx = [n for n in NEWS_NAMES if n in ctx]
    news_glb = [n for n in NEWS_NAMES if n in glob]
    vendor_glb = [n for n in VENDOR_NAMES if n in glob]

    # Modules already loaded by the runtime (e.g. xtquant) are visible without
    # importing; a name here is NOT proof it is reachable from a strategy.
    mod_hits = sorted([m for m in mods
                       if any(k in m.lower() for k in
                              ("xtquant", "xtdata", "news", "announce", "wind",
                               "tushare", "juyuan", "requests", "urllib"))])

    for n in ctrl_ctx + list(set(ctrl_glb) - set(ctrl_ctx)):
        print("PROBE_CONTROL_PRESENT %s" % n)
    for n in sorted(set(data_ctx + data_glb)):
        print("PROBE_DATA_PRESENT %s" % n)
    for n in sorted(set(news_ctx + news_glb)):
        print("PROBE_NEWS_PRESENT %s" % n)
    for n in vendor_glb:
        print("PROBE_VENDOR_PRESENT %s" % n)
    print("PROBE_MODULE_KEYS %s" % (",".join(mod_hits) if mod_hits else "none"))

    controls = len(set(ctrl_ctx + ctrl_glb))
    reliable = 1 if controls >= 2 else 0
    print("PROBE_SELFTEST controls_seen=%d reliable=%d" % (controls, reliable))
    print("PROBE_RESULT news_api=%d data_api=%d vendor_name=%d reliable=%d" % (
        len(set(news_ctx + news_glb)), len(set(data_ctx + data_glb)),
        len(vendor_glb), reliable))
    if reliable == 0:
        print("PROBE_RESULT_INVALID detector_broken: negative result is NOT usable")
    print("PROBE_DONE paste_all_PROBE_lines_back")


def stop(ContextInfo):
    """No-op shutdown hook required by some QMT builds."""
    return None
