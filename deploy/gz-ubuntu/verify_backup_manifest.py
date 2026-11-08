#!/usr/bin/env python3
# 日常全目录备份归档的逐文件复算器（方案 §6.6 工单 ② 的「manifest 复算 0 差异」判据）。
# 解压归档到隔离临时目录，按归档内 MANIFEST.json 的 files[] 逐条复算 bytes/sha256/mode，
# 并检查归档成员集合与清单一致。只用归档自己的清单当尺子，不另造一套哈希逻辑。
# 用法：python3 verify_backup_manifest.py <archive.tar.gz>
import hashlib
import json
import os
import stat
import sys
import tarfile
import tempfile


# 服务运行中会持续写的文件：manifest 在 tar 之前生成，归档里的内容可能已变。
# 按方案 §7.3「允许差异白名单」逐条登记路径 + 原因 + 原文读数，不笼统忽略。
def live_write_reason(rel):
    if rel == "scheduler_status.json":
        return "engine_scheduler_tick"
    if rel.startswith("task_logs/"):
        return "research_task_log_append"
    return ""


def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def main(archive):
    if not os.path.isfile(archive):
        print("MANIFEST_VERIFY_RESULT=FAIL reason=archive_absent")
        return 1
    with tempfile.TemporaryDirectory(prefix="quant-manifest-verify-") as tmp:
        with tarfile.open(archive, "r:gz") as tf:
            tf.extractall(tmp, filter="data")
        man_path = os.path.join(tmp, "MANIFEST.json")
        if not os.path.isfile(man_path):
            print("MANIFEST_VERIFY_RESULT=FAIL reason=manifest_absent")
            return 1
        doc = json.load(open(man_path, encoding="utf-8"))
        files = doc.get("files", [])
        diffs = []
        mode_drift = []
        live_writes = []
        for e in files:
            path = os.path.join(tmp, e["rel_path"])
            if not os.path.isfile(path):
                diffs.append("MISSING|" + e["rel_path"])
                continue
            st = os.stat(path)
            live = live_write_reason(e["rel_path"])
            if st.st_size != int(e["bytes"]):
                line = "SIZE_DIFF|%s|want=%s|got=%s" % (
                    e["rel_path"], e["bytes"], st.st_size)
                (live_writes if live else diffs).append(
                    line + ("|" + live if live else ""))
                continue
            if digest(path) != e["sha256"]:
                line = "CONTENT_DIFF|" + e["rel_path"]
                (live_writes if live else diffs).append(
                    line + ("|" + live if live else ""))
                continue
            # mode 只登记不判红：manifest 记的是源目录权限（多为 0666），
            # tar 解压按 umask 重建，两者本就不等。按方案 §7.3 显式列入允许
            # 差异白名单并留计数，内容与字节差异仍严格判红。
            if e.get("mode") and oct(stat.S_IMODE(st.st_mode)) != e["mode"]:
                mode_drift.append(e["rel_path"])
        # 归档成员集合 vs 清单（tar 把数据根写成 ./xxx，清单里是 xxx）
        with tarfile.open(archive, "r:gz") as tf:
            members = {m.name.lstrip("./") for m in tf.getmembers() if m.isfile()}
        declared = {e["rel_path"] for e in files}
        declared.add("MANIFEST.json")
        declared.add("SNAPSHOT_OK")
        extra = sorted(members - declared)
        absent = sorted(declared - members)
        print("MANIFEST_MODE_DRIFT=%d reason=tar_extract_umask_allowed" % len(mode_drift))
        print("MANIFEST_LIVE_WRITES_ALLOWED=%d reason=services_running_paths_registered" % len(live_writes))
        for line in live_writes[:10]:
            print("MANIFEST_LIVE_WRITE|" + line)
        print("MANIFEST_VERIFY files=%d excluded=%d diffs=%d extra=%d absent=%d" % (
            len(files), len(doc.get("excluded", [])), len(diffs), len(extra), len(absent)))
        for line in diffs[:10]:
            print("MANIFEST_FAIL|" + line)
        for line in extra[:10]:
            print("MANIFEST_EXTRA|" + line)
        for line in absent[:10]:
            print("MANIFEST_ABSENT|" + line)
        ok = not diffs and not extra and not absent
        print("MANIFEST_VERIFY_RESULT=%s" % ("OK" if ok else "FAIL"))
        return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else ""))
