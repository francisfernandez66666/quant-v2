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
        for e in files:
            path = os.path.join(tmp, e["rel_path"])
            if not os.path.isfile(path):
                diffs.append("MISSING|" + e["rel_path"])
                continue
            st = os.stat(path)
            if st.st_size != int(e["bytes"]):
                diffs.append("SIZE_DIFF|%s|want=%s|got=%s" % (
                    e["rel_path"], e["bytes"], st.st_size))
                continue
            if digest(path) != e["sha256"]:
                diffs.append("CONTENT_DIFF|" + e["rel_path"])
                continue
            if e.get("mode") and oct(stat.S_IMODE(st.st_mode)) != e["mode"]:
                diffs.append("MODE_DIFF|" + e["rel_path"])
        # 归档成员集合 vs 清单（tar 把数据根写成 ./xxx，清单里是 xxx）
        with tarfile.open(archive, "r:gz") as tf:
            members = {m.name.lstrip("./") for m in tf.getmembers() if m.isfile()}
        declared = {e["rel_path"] for e in files}
        declared.add("MANIFEST.json")
        declared.add("SNAPSHOT_OK")
        extra = sorted(members - declared)
        absent = sorted(declared - members)
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
