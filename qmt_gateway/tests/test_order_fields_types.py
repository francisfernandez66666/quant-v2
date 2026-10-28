#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FIX-20261028-F16（2026-10-29）契约 golden 的**类型面**自洽校验。

背景：Go 侧 internal/trading/order_contract_test.go 已把 `types` 纳入 golden 并与反射逐字段
比对（改类型必须同步 golden）。本文件补网关这一侧的"读同一份契约、不自我分家"：

  · types 的键集必须与 fields 完全相等（既不漏字段，也不留幽灵键）；
  · fields 必须等于 consumed ∪ ignored（与 Go 侧同一条闭合关系）；
  · 类型串只允许出现在约定集合里（Go 侧反射产物：string/int/int64/float64/bool，
    指针记 ptr:，切片记 slice:）——出现新形态说明有人加了复杂类型，
    网关解析器需要同步评估，不能默认它"能解"。

为什么不在这一侧真的去比 Python 类型注解：网关是动态取值（float()/int() 转换），
没有可反射的类型系统；拿"字段名 + 允许集合 + 闭合关系"三件事锁住就够了，
真正的行为面（值解析错了）由 test_order_gates.py 的行为用例负责。
"""
import json
import pathlib

CONTRACT = pathlib.Path(__file__).resolve().parents[1] / "contract" / "order_fields.json"

# Go 侧 goTypeString 的产物形态（含 ptr:/slice: 前缀）
ALLOWED_TYPES = {
    "string", "int", "int64", "float64", "bool",
    "ptr:string", "ptr:int", "ptr:int64", "ptr:float64", "ptr:bool",
    "slice:string", "slice:int", "slice:int64", "slice:float64",
}


def _doc():
    return json.loads(CONTRACT.read_text(encoding="utf-8"))


def test_types_cover_exact_field_set():
    doc = _doc()
    assert "types" in doc, "golden.types 缺失：类型锁未启用（用 ORDER_CONTRACT_UPDATE=1 重新生成）"
    assert set(doc["types"].keys()) == set(doc["fields"]), (
        "types 与 fields 键集不一致："
        f"多出={sorted(set(doc['types']) - set(doc['fields']))[:5]} "
        f"缺={sorted(set(doc['fields']) - set(doc['types']))[:5]}"
    )


def test_consumed_plus_ignored_equals_fields():
    doc = _doc()
    union = set(doc["consumed_by_gateway"]) | set(doc["ignored_by_gateway"])
    assert union == set(doc["fields"]), "consumed ∪ ignored 必须恰好覆盖 fields（与 Go 侧同一条闭合关系）"
    assert not (set(doc["consumed_by_gateway"]) & set(doc["ignored_by_gateway"])), (
        "同一字段不得同时出现在 consumed 与 ignored"
    )


def test_type_strings_are_known_shapes():
    doc = _doc()
    unknown = {k: v for k, v in doc["types"].items() if v not in ALLOWED_TYPES}
    assert not unknown, f"出现未登记的类型形态（网关解析器需同步评估）: {unknown}"
