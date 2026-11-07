#!/usr/bin/env python3
# backup_gz_cold_helper.py -- the measuring instrument behind scripts/backup_gz_cold.sh.
#
# WHY THIS FILE EXISTS AND WHY IT IS PURE ASCII
#   docs/PLAN_GZ_REINSTALL_BACKUP_20261104.md 1.2 says the current Guangzhou backup chain
#   (deploy/qmt-win/backup_snapshot.ps1 fixed list + scripts/backup.sh fixed list) is
#   guaranteed to miss files, because Go writes dozens of runtime artifacts into <DataDir>
#   and the 11-04 batch added more (manual_positions/manual_account, accounts/<uid>/
#   leg_status.json, signal cards). So the cold backup enumerates the ACTUAL tree and the
#   enumeration is the input to the payload, never a hand-typed name list.
#   This helper is the part that needs real JSON/SQLite/tar handling. It runs in two shapes:
#     - on the Mac, against a local fixture tree (rehearsal, and eventually --local-source)
#     - ON THE GUANGZHOU BOX, fed over ssh stdin:  ssh ... "python - --job=<base64>"
#   The second shape is why every byte here is ASCII and every printed line is a
#   machine-readable anchor: a Chinese literal going through PowerShell on a cp936 box
#   decodes into '?' and turns a judgement line into a permanent false green
#   (AGENTS.md, 11-02 batch, hit twice the same day). Comments are English for the same
#   reason -- this file must be safe to pipe through a GBK console.
#
# NO NEW RULERS (sec 1.4 "do not invent a second measuring stick")
#   SQLite consistency  -> the caller runs deploy/qmt-win/backup_snap.py (sqlite3 .backup API
#                          + PRAGMA integrity_check). A bare cp of a running WAL db is a torn
#                          copy; nothing in this file does that.
#   per-file sha256     -> same discipline as scripts/build_friend_package.sh (MANIFEST that
#                          does not hash itself; verify-on-arrival; rebuild-and-recheck).
#   accounts_files      -> cross-checked against the SNAPSHOT_OK marker that
#                          backup_snapshot.ps1 already produces.
#   row counts          -> the tables named in plan 7.2 L2 / 5 S3.3, nothing else.
#
# OUTPUT PROTOCOL (every line ASCII, parsed by backup_gz_cold.sh)
#   HELP_RESULT=OK mode=<m> / HELP_RESULT=FAIL mode=<m> reason=<code>
#   ENUM_SUMMARY|tag=<t>|files=<n>|bytes=<n>|excluded=<n>|accounts_files=<n>|hashed=<0|1>
#   V|name=<n>|bytes=<b>|sha256=<h>|members=<m>|arcname=<a>
#   BUILD_RESULT=OK volumes=<n> bytes=<b>
#   L1_<KIND>|... with KIND in MISSING EXTRA CONTENT MODE SIZE DUP DELETED EXCLUDED_PRESENT
#   L1_RESULT=OK files=<n> diffs=0 shadowed=<n>  /  L1_RESULT=FAIL ... details=<n>
#   L2|db=<n>|integrity=ok|fk_clean=<0|1>|rows=<t>:<n>,...
#   L2_RESULT=OK|FAIL ... fails=<n>
#   SCAN_SUMMARY|changed=<n>|added=<n>|deleted=<n>|config=<n>
#   MANIFEST_RESULT=OK path=<p> version=<v> files=<n> bytes=<n>
#   A trailing <base64> line only appears for JSON the caller must fetch; bulk JSON always
#   lands as a file under job["work"] (that is the remote staging dir in ssh mode, so the
#   caller scp's it back instead of streaming megabytes through a console).
#
# JOB IS PASSED AS ONE BASE64 ARGUMENT (job JSON, ASCII)
#   argv after "--job=" -- never a long list of quoted paths: cmd.exe does not treat single
#   quotes as quoting and PowerShell has its own rules, so an argument list with '*' or '='
#   is exactly where this repo has been bitten before. Base64 of the JSON alphabet
#   [A-Za-z0-9+/=] contains no shell metacharacters.
#
# FAILURE MODES THE CALLER MUST READ (all of them print HELP_RESULT=FAIL, never a silent
# skip -- "it ran" is not "it worked"):
#   reason=bad_job / unknown_mode / no_roots / missing_enum_json / dup_rel_path /
#   volume_build_failed / missing_manifest / verify_target_absent / l2_db_absent /
#   l2_integrity_bad / l2_table_absent / scan_target_absent / merge_input_bad
import argparse
import base64
import binascii
import fnmatch
import gzip
import hashlib
import io
import json
import os
import shutil
import sqlite3
import sys
import tarfile
import tempfile

CHUNK = 1 << 20
# Which database holds which of the tables plan 5 S3.3 / 7.2 L2 names. The 11-04 manual
# book (manual_positions/manual_account) lives in the live ledger, signal_decisions in the
# research db; a mapped table that has vanished is a finding, not a zero.
L2_DB_TABLES = {
    "live.db": ["real_positions", "orders", "fills", "real_account",
                "manual_positions", "manual_account"],
    "trading.db": ["signal_decisions"],
}
L2_DB_ALL = sorted(L2_DB_TABLES)
# Exclusion reasons whose rel_path is legitimately present in the package again under a
# different provenance (the raw db is replaced by its online .backup snapshot, same path).
REPLACED_EXCLUSION_REASONS = {"db_raw_replaced_by_online_snapshot"}
# plan 4.3: the approval products. Losing them means re-running optimisation + approval.
L2_APPLIED = ["applied_rules.json", "applied_factors.json",
              "applied_patterns.json", "grayscale_rules.json"]
# plan 3.1: auth.json is the ONLY container of user configuration (users[], configs[],
# invites, tenants, schema_version). We record three conservation readings, never contents.
L2_AUTH_KEYS = ["users", "configs", "invites", "tenants"]
# plan 5 S3b: a config change after frozen_at must be explicitly registered before v2 is
# allowed. "Close enough" is what the doc forbids, so this pattern list decides which
# changed paths count as CONFIG (identity/rules/ledger wiring) versus ordinary data drift.
CONFIG_GLOBS = ["auth.json", "config.json", "admin_session_token",
                "config_history/*", "applied_rules.json", "applied_factors.json",
                "applied_patterns.json", "grayscale_rules.json",
                "accounts/*/rules.json", "accounts/*/qmt*.json"]


def out(line):
    """Print one ASCII protocol line. Refuses to emit non-ASCII rather than trusting the
    console code page to survive it (the '?' mangling is silent, so the guard must be here)."""
    text = str(line)
    try:
        text.encode("ascii")
    except UnicodeEncodeError:
        text = text.encode("ascii", "replace").decode("ascii")
    sys.stdout.write(text + "\n")
    sys.stdout.flush()


def fail(mode, reason, extra=""):
    out("HELP_RESULT=FAIL mode=%s reason=%s%s" % (mode, reason, (" " + extra) if extra else ""))
    return 1


def ok(mode, extra=""):
    out("HELP_RESULT=OK mode=%s%s" % (mode, (" " + extra) if extra else ""))
    return 0


def sha256_path(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for block in iter(lambda: fh.read(CHUNK), b""):
            h.update(block)
    return h.hexdigest()


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def file_entry(abs_path, rel_path, want_hash=True):
    """One manifest file record, exactly the five fields plan 5 S2 lists for files[].
    mtime is epoch seconds (no timezone games across Windows/Linux); mode is the raw
    st_mode low bits as an octal string -- see the normalisation note in mode_matches."""
    st = os.stat(abs_path)
    return {
        "rel_path": rel_path,
        "bytes": int(st.st_size),
        "sha256": sha256_path(abs_path) if want_hash else "-",
        "mtime": int(st.st_mtime),
        "mode": oct(st.st_mode & 0o7777),
    }


def norm_mode(value):
    """Canonicalise a mode string for comparison.

    On Windows st_mode only distinguishes 'read-only' from 'writable' (NTFS ACLs are not
    POSIX bits), and extracting a tar honouring the local umask changes the trailing bits.
    Comparing the raw value across that boundary would produce MODE_DIFF noise on every
    file, and a check that always fires is a check nobody reads. So: archive-side
    comparisons (source mode vs tar member mode, both untouched) use the full value;
    extracted-directory comparisons use the owner bits only and say so in the report."""
    try:
        return int(str(value), 8)
    except ValueError:
        return -1


def mode_matches(want, got, full=True):
    w, g = norm_mode(want), norm_mode(got)
    if w < 0 or g < 0:
        return False
    return (w == g) if full else ((w & 0o700) == (g & 0o700))


def load_json(path):
    with io.open(path, encoding="utf-8") as fh:
        return json.load(fh)


def dump_json(path, data):
    """Deterministic JSON: sorted keys, LF, no trailing whitespace -- so the file's own
    sha256 is reproducible and the archive can be re-checked byte for byte."""
    parent = os.path.dirname(os.path.abspath(path))
    if parent and not os.path.isdir(parent):
        os.makedirs(parent)
    with io.open(path, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(json.dumps(data, ensure_ascii=True, sort_keys=True, indent=1))
        fh.write("\n")


def walk_root(root):
    """Recursive enumeration of one source root: the whole directory, not a name list.
    plan 1.2 / R4 is the reason this function refuses to know any file names: whatever the
    next batch adds to <DataDir> is carried without anyone editing a script.

    Symlinks are recorded as skipped (a link into another volume would make the archive
    non-portable); sockets/fifos are refused outright rather than read (reading a fifo
    hangs -- the repo has been bitten by a hanging read leg before)."""
    base = root["path"]
    prefix = root.get("prefix", "")
    group = root.get("group", "data")
    want_hash = bool(root.get("hash", True))
    excludes = [(str(p), str(r)) for p, r in root.get("exclude", [])]
    only = [str(x) for x in root.get("only", [])]
    entries, excluded, junk = [], [], []
    if not os.path.isdir(base):
        return None, excluded, ["root_absent=%s" % base]
    for cur, dirs, names in os.walk(base):
        dirs.sort()
        for name in sorted(names):
            abs_path = os.path.join(cur, name)
            rel = os.path.relpath(abs_path, base).replace(os.sep, "/")
            rel_path = prefix + rel
            if only and rel not in only:
                continue
            reason = None
            for pat, cat in excludes:
                if fnmatch.fnmatch(rel, pat) or fnmatch.fnmatch(rel_path, pat):
                    reason = cat
                    break
            if reason is not None:
                # An exclusion WITHOUT a reason category is how a missing file later reads
                # as "it was never there" (plan 4.7). Every skip carries its category.
                excluded.append({"rel_path": rel_path, "reason": reason})
                continue
            try:
                if os.path.islink(abs_path):
                    junk.append("symlink_skipped=%s" % rel_path)
                    continue
                st = os.lstat(abs_path)
                if not os.path.isfile(abs_path):
                    junk.append("not_a_regular_file=%s" % rel_path)
                    continue
                del st
                entries.append(dict(file_entry(abs_path, rel_path, want_hash),
                                    group=group, abs=abs_path,
                                    content_only=bool(root.get("content_only", False))))
            except OSError as exc:
                # Unreadable file: this is a finding, not a skip. Silently dropping it would
                # make the baseline look complete while a P0 artifact is missing.
                junk.append("stat_failed=%s:%s" % (rel_path, type(exc).__name__))
        for name in sorted(dirs):
            abs_dir = os.path.join(cur, name)
            rel_dir = os.path.relpath(abs_dir, base).replace(os.sep, "/") + "/"
            for pat, cat in excludes:
                if fnmatch.fnmatch(rel_dir, pat.rstrip("/") + "/*"):
                    excluded.append({"rel_path": prefix + rel_dir.rstrip("/"),
                                     "reason": cat, "kind": "dir"})
                    dirs.remove(name)
                    break
    return entries, excluded, junk


def accounts_file_count(entries):
    return sum(1 for e in entries if e["rel_path"].startswith("accounts/"))


# -- mode: enumerate ----------------------------------------------------------------
def mode_enumerate(job):
    work = job.get("work")
    tag = job.get("tag", "data")
    roots = job.get("roots", [])
    if not work or not roots:
        return fail("enumerate", "no_roots")
    all_entries, all_excluded, all_junk = [], [], []
    for root in roots:
        entries, excluded, junk = walk_root(root)
        if entries is None:
            return fail("enumerate", "root_absent", "path=%s" % root.get("path"))
        all_entries.extend(entries)
        all_excluded.extend(excluded)
        all_junk.extend(junk)
    seen = set()
    for e in all_entries:
        if e["rel_path"] in seen:
            # Two roots producing the same rel_path would make the archive ambiguous:
            # whichever tar ran last would silently win. Refuse instead.
            return fail("enumerate", "dup_rel_path", "rel=%s" % e["rel_path"])
        seen.add(e["rel_path"])
    payload = {
        "tag": tag,
        "hashed": bool(all_entries and all(e["sha256"] != "-" for e in all_entries)),
        "entries": sorted(all_entries, key=lambda x: x["rel_path"]),
        "excluded": sorted(all_excluded, key=lambda x: x["rel_path"]),
        "totals": {
            "files": len(all_entries),
            "bytes": sum(int(e["bytes"]) for e in all_entries),
            "accounts_files": accounts_file_count(all_entries),
        },
    }
    out_path = os.path.join(work, "enum_%s.json" % tag)
    dump_json(out_path, payload)
    out("ENUM_SUMMARY|tag=%s|files=%d|bytes=%d|excluded=%d|accounts_files=%d|hashed=%d|out=%s" % (
        tag, payload["totals"]["files"], payload["totals"]["bytes"],
        len(payload["excluded"]), payload["totals"]["accounts_files"],
        1 if payload["hashed"] else 0, out_path))
    for line in all_junk[:20]:
        out("ENUM_NOTE|%s" % line)
    if all_junk:
        out("ENUM_NOTES|count=%d" % len(all_junk))
    return ok("enumerate", "files=%d" % payload["totals"]["files"])


# -- mode: build (volume assignment + tar.gz production) ----------------------------
def human_label(rel):
    return "".join(c if (c.isalnum() or c in "._-") else "_" for c in rel)[:24] or "files"


def assign_volumes(entries, volume_max_bytes, big_min_bytes):
    """Split the payload into volumes (plan 5 S2: 'directory per volume, big DBs by size').

    A failed volume is re-transferred alone, so volume size IS the blast radius of a
    transfer failure -- and the reason the two multi-GB databases must not share a volume
    with the small config files."""
    volumes = []
    groups = {}
    for e in entries:
        groups.setdefault(e.get("group", "data"), []).append(e)
    for group in sorted(groups):
        items = sorted(groups[group], key=lambda x: x["rel_path"])
        big = [e for e in items if int(e["bytes"]) >= big_min_bytes]
        rest = [e for e in items if int(e["bytes"]) < big_min_bytes]
        for e in big:
            volumes.append({"label": "%s_%s" % (group, human_label(e["rel_path"])),
                            "group": group, "members": [e]})
        bucket, bucket_bytes = [], 0
        for e in rest:
            if bucket and bucket_bytes + int(e["bytes"]) > volume_max_bytes:
                volumes.append({"label": "%s_part%03d" % (group, len(
                    [v for v in volumes if v["group"] == group]) + 1),
                                "group": group, "members": bucket})
                bucket, bucket_bytes = [], 0
            bucket.append(e)
            bucket_bytes += int(e["bytes"])
        if bucket:
            volumes.append({"label": "%s_part%03d" % (group, len(
                [v for v in volumes if v["group"] == group]) + 1),
                            "group": group, "members": bucket})
    return volumes


def mode_build(job):
    enum_files = job.get("enum_files", [])
    work = job.get("work")
    if not enum_files or not work:
        return fail("build", "missing_enum_json")
    entries = []
    for path in enum_files:
        if not os.path.isfile(path):
            return fail("build", "missing_enum_json", "path=%s" % path)
        entries.extend(load_json(path)["entries"])
    volume_max = int(job.get("volume_max_bytes", 2 * 1024 * 1024 * 1024))
    big_min = int(job.get("big_file_min_bytes", 512 * 1024 * 1024))
    compress = bool(job.get("compress", True))
    out_dir = job.get("volumes_dir") or os.path.join(work, "volumes")
    bad = guard_stage_dir(work, out_dir, "build")
    if bad:
        return bad
    if not os.path.isdir(out_dir):
        os.makedirs(out_dir)
    # A previous run's leftovers must not masquerade as this run's products: if a stale tar
    # is still here, its sha would be recorded and the caller would verify a file it never
    # produced. Only inside the staging dir guarded above, and only *.tar(.gz).
    for stale in os.listdir(out_dir):
        if stale.endswith(".tar.gz") or stale.endswith(".tar"):
            os.unlink(os.path.join(out_dir, stale))
    records = []
    if job.get("keep_records") and os.path.isfile(
            job.get("volumes_out") or os.path.join(work, "volumes.json")):
        records = load_json(job["volumes_out"])["volumes"]
    plan = assign_volumes(entries, volume_max, big_min)
    if job.get("plan_only"):
        # Dry run: report the assignment (name / member count / uncompressed content bytes)
        # and write NOTHING -- not even volumes.json. The real run is the only path that
        # touches the disk layout or the network.
        total = 0
        for idx, vol in enumerate(plan, start=int(job.get("index_start", 1))):
            size = sum(int(e["bytes"]) for e in vol["members"])
            total += size
            out("VPLAN|index=%02d|label=%s|members=%d|content_bytes=%d" % (
                idx, vol["label"], len(vol["members"]), size))
        out("BUILD_PLAN_RESULT=OK volumes=%d content_bytes=%d" % (len(plan), total))
        return ok("build", "plan_only volumes=%d" % len(plan))
    total_bytes = sum(int(r["bytes"]) for r in records)
    total_bytes = sum(int(r["bytes"]) for r in records)
    for idx, vol in enumerate(plan, start=int(job.get("index_start", 1))):
        name = "v%02d_%s.tar%s" % (idx, vol["label"], ".gz" if compress else "")
        path = os.path.join(out_dir, name)
        tmp = path + ".part"
        try:
            opener = gzip.open if compress else open
            with opener(tmp, "wb") as raw:
                with tarfile.open(fileobj=raw, mode="w") as tf:
                    for e in vol["members"]:
                        if not os.path.isfile(e["abs"]):
                            raise IOError("member_absent:" + e["rel_path"])
                        # arcname = the <DataDir>-relative path, so the restore side maps
                        # 1:1 onto the target layout (plan 5 S2 'volume keeps relative paths').
                        tf.add(e["abs"], arcname=e["rel_path"], recursive=False)
            os.replace(tmp, path)
        except Exception as exc:  # noqa: BLE001 - the failure text is the finding
            if os.path.exists(tmp):
                os.unlink(tmp)
            return fail("build", "volume_build_failed", "vol=%s err=%s" % (
                name, type(exc).__name__ + ":" + str(exc)[:120]))
        size = os.path.getsize(path)
        digest = sha256_path(path)
        record = {"name": name, "path": path, "label": vol["label"], "seq": idx,
                  "group": vol["group"], "bytes": size, "sha256": digest,
                  "members": len(vol["members"]),
                  "content_bytes": sum(int(e["bytes"]) for e in vol["members"]),
                  "rel_paths": [e["rel_path"] for e in vol["members"]]}
        records = [r for r in records if r["name"] != name] + [record]
        total_bytes += size
        out("V|name=%s|bytes=%d|sha256=%s|members=%d|arcname=%s" % (
            name, size, digest, len(vol["members"]), vol["label"]))
    total_bytes = sum(int(r["bytes"]) for r in records)
    dump_json(job.get("volumes_out") or os.path.join(work, "volumes.json"),
              {"volumes": sorted(records, key=lambda r: (r["seq"], r["name"])),
               "total_bytes": total_bytes})
    out("BUILD_RESULT=OK volumes=%d bytes=%d out_dir=%s" % (len(records), total_bytes, out_dir))
    return ok("build", "volumes=%d" % len(records))


# -- mode: manifest -----------------------------------------------------------------
def mode_manifest(job):
    work = job.get("work")
    out_name = job.get("out_name", "BACKUP_MANIFEST.json")
    if not work:
        return fail("manifest", "bad_job")
    files, excluded, totals_files = [], [], {}
    for path in job.get("enum_files", []):
        payload = load_json(path)
        files.extend(payload["entries"])
        excluded.extend(payload.get("excluded", []))
        totals_files[payload.get("tag", "x")] = payload["totals"]
    # rel_path must be unique across the merged set (v2 merging depends on it).
    seen = set()
    for e in files:
        if e["rel_path"] in seen:
            return fail("manifest", "dup_rel_path", "rel=%s" % e["rel_path"])
        seen.add(e["rel_path"])
    for e in files:
        e.pop("abs", None)
        e.setdefault("group", "data")
    volumes = load_json(job["volumes_json"])["volumes"] if job.get("volumes_json") else []
    for v in volumes:
        v.pop("rel_paths", None)
    if job.get("secrets_json"):
        secrets = load_json(job["secrets_json"])
    else:
        secrets = job.get("secrets") or {"secrets": []}
    header = job.get("header", {})
    # The manifest never hashes itself (build_friend_package.sh:197-236 got this exactly
    # wrong first and had to re-walk after purging); it is excluded from files[] by name.
    files = [e for e in files if os.path.basename(e["rel_path"]) != out_name]
    doc = {
        "manifest_version": int(job.get("version", 1)),
        "header": header,
        "files": sorted(files, key=lambda x: x["rel_path"]),
        "excluded": sorted(excluded, key=lambda x: x["rel_path"]),
        "volumes": sorted(volumes, key=lambda x: (int(x.get("seq", 0)), x["name"])),
        "secrets": sorted(secrets.get("secrets", []), key=lambda x: x["path"]),
        "deleted": sorted(job.get("deleted", [])),
        "previous_manifest_sha256": job.get("previous_manifest_sha256", ""),
        "totals": {
            "file_count": len(files),
            "data_total_bytes": sum(int(e["bytes"]) for e in files),
            "excluded_count": len(excluded),
            "volume_count": len(volumes),
            "volume_bytes": sum(int(v["bytes"]) for v in volumes),
            "secrets_count": len(secrets.get("secrets", [])),
        },
    }
    path = os.path.join(work, out_name)
    dump_json(path, doc)
    out("MANIFEST_RESULT=OK path=%s version=%d files=%d bytes=%d" % (
        path, doc["manifest_version"], doc["totals"]["file_count"],
        doc["totals"]["data_total_bytes"]))
    return ok("manifest", "files=%d" % doc["totals"]["file_count"])


def volume_order(doc, job):
    """The volume names L1/L2 read, oldest first.

    For manifest v2 the delta volumes MUST come after the v1 ones: a path superseded by
    the increment is present twice in the set (the v1 copy is the audit chain), and "the
    newest copy is the baseline" is exactly what the order encodes. v1's own manifest never
    lists the delta volumes, so re-verifying v1 with its own manifest still reads only v1."""
    names = [v["name"] for v in sorted(doc.get("volumes", []),
                                       key=lambda x: (int(x.get("seq", 0)), x["name"]))]
    for extra in job.get("extra_volume_names", []):
        if extra not in names:
            names.append(extra)
    return names


def guard_stage_dir(work, target, mode):
    """Refuse to write/delete outside the caller's own staging directory.

    The build step purges stale *.tar.gz before producing volumes. That purge is only safe
    inside the staging area this backup created; a wrong --volumes_dir pointing at the data
    directory would otherwise delete real files. Safety rail: no generic rm anywhere."""
    a = os.path.abspath(work)
    b = os.path.abspath(target)
    if b != a and not b.startswith(a + os.sep):
        return fail(mode, "unsafe_stage_dir", "target=%s work=%s" % (b, a))
    return None


# -- archive / directory iteration (one comparator, two producers) ------------------
def resolve_volume(job, name):
    """Where a volume archive actually lives on the machine running this helper.

    v2 (the S3b increment) must NOT copy the sealed v1 baseline -- the Mac had 11 Gi free at
    planning time, so a second full copy is exactly what produces a half-written backup (R1).
    Instead the caller maps each volume name to its own path: v1 volumes stay read-only in
    <baseline>/volumes, the delta lands in <baseline>/volumes-delta."""
    paths = job.get("volume_paths") or {}
    if name in paths:
        return paths[name]
    base = job.get("volumes_dir") or "."
    return os.path.join(base, name)


def iter_archives(job, volume_names):
    """Yield (rel_path, bytes, sha256, mode, where) from the tar(.gz) volumes in the order
    given. Reading the archive instead of extracting it is deliberate: the Mac had 11 Gi
    free at planning time (plan 1.3), and extracting a multi-GB baseline next to its own
    volumes is how R1 turns into a half-written backup."""
    for name in volume_names:
        path = resolve_volume(job, name)
        if not os.path.isfile(path):
            raise IOError("volume_absent:" + name)
        with tarfile.open(path, mode="r:*") as tf:
            for member in tf.getmembers():
                if not member.isfile():
                    continue
                handle = tf.extractfile(member)
                if handle is None:
                    continue
                h = hashlib.sha256()
                size = 0
                while True:
                    block = handle.read(CHUNK)
                    if not block:
                        break
                    size += len(block)
                    h.update(block)
                yield member.name, size, h.hexdigest(), oct(member.mode & 0o7777), name


def iter_dir(root_dir):
    for cur, _dirs, names in os.walk(root_dir):
        for name in sorted(names):
            abs_path = os.path.join(cur, name)
            rel = os.path.relpath(abs_path, root_dir).replace(os.sep, "/")
            yield rel, int(os.stat(abs_path).st_size), sha256_path(abs_path), \
                oct(os.stat(abs_path).st_mode & 0o7777), rel


# -- mode: verify (L1, byte level) --------------------------------------------------
def mode_verify(job):
    mode_kind = "archive" if job.get("volumes_dir") else "dir"
    manifest_path = job.get("manifest")
    if not manifest_path or not os.path.isfile(manifest_path):
        return fail("verify", "missing_manifest")
    doc = load_json(manifest_path)
    expected = {e["rel_path"]: e for e in doc["files"]}
    deleted = set(doc.get("deleted", []))
    excluded = {}
    for item in doc.get("excluded", []):
        excluded[item["rel_path"]] = item.get("reason", "other")
    full_mode = mode_kind == "archive"
    found = {}
    shadowed = 0
    names = volume_order(doc, job)
    try:
        if mode_kind == "archive":
            for rel, size, digest, mmode, where in iter_archives(job, names):
                if rel in found and found[rel][2] != digest:
                    # Same path, different bytes across the volume set: the newest copy
                    # (later in `names`) wins, the older one stays as the audit chain.
                    # Counted and reported, never silently overwritten.
                    shadowed += 1
                found[rel] = (size, digest, mmode, where)
        else:
            for rel, size, digest, mmode, where in iter_dir(job["dir"]):
                found[rel] = (size, digest, mmode, where)
    except IOError as exc:
        return fail("verify", "archive_unreadable", "err=%s" % str(exc)[:120])

    diffs = []
    for rel, want in sorted(expected.items()):
        got = found.get(rel)
        if got is None:
            diffs.append(("L1_MISSING", rel, ""))
            continue
        size, digest, mmode, where = got
        if digest != want["sha256"]:
            diffs.append(("L1_CONTENT", rel,
                          "expect=%s got=%s in=%s" % (want["sha256"][:16], digest[:16], where)))
        if size != int(want["bytes"]):
            diffs.append(("L1_SIZE", rel, "expect=%s got=%d" % (want["bytes"], size)))
        if not mode_matches(want["mode"], mmode, full_mode):
            diffs.append(("L1_MODE", rel, "expect=%s got=%s%s" % (
                want["mode"], mmode, "" if full_mode else " (owner-bits only)")))
    for rel in sorted(found):
        if rel in expected:
            continue
        if rel in deleted:
            # plan 5 S3b: v1 stays as the audit chain, so a superseded/deleted path is
            # STILL physically present in an older volume. That is not an EXTRA finding --
            # but it must be named, or the next reader cannot tell "deleted" from "lost".
            out("L1_DELETED_KEPT_IN_AUDIT|%s|in=%s" % (rel, found[rel][3]))
            continue
        diffs.append(("L1_EXTRA", rel, "in=%s" % found[rel][3]))
    if job.get("check_excluded"):
        # plan 4.7 / R11: "an exclusion that is actually inside the package is worse than no
        # exclusion at all" -- the exclusion list is what lets a later reader tell an
        # intentional skip from a lost file, so the skip has to be real.
        # One reason category is exempt by construction: the raw <db> path is excluded and
        # the SAME rel_path is then carried as the online .backup snapshot, so presence
        # there is the design, not a leak (plan 4.2 "no bare cp of a running db").
        for rel in sorted(excluded):
            if excluded[rel] in REPLACED_EXCLUSION_REASONS:
                continue
            hit = sorted(k for k in found if k == rel or k.startswith(rel + "/"))
            if hit:
                diffs.append(("L1_EXCLUDED_PRESENT", rel,
                              "reason=%s present_files=%d first=%s" % (
                                  excluded[rel], len(hit), hit[0])))
    for kind, rel, detail in diffs[:200]:
        out("%s|%s|%s" % (kind, rel, detail))
    counts = {}
    for kind, _rel, _d in diffs:
        counts[kind] = counts.get(kind, 0) + 1
    summary = " ".join("%s=%d" % (k.replace("L1_", "").lower(), v)
                       for k, v in sorted(counts.items()))
    if diffs:
        out("L1_RESULT=FAIL files=%d diffs=%d details=%d %s shadowed=%d" % (
            len(expected), len(diffs), min(len(diffs), 200), summary, shadowed))
        return 1
    out("L1_RESULT=OK files=%d diffs=0 shadowed=%d mode_compare=%s" % (
        len(expected), shadowed, "full" if full_mode else "owner_bits"))
    return ok("verify", "diffs=0")


# -- mode: l2 (structural level) ----------------------------------------------------
def sqlite_readings(db_path, tables):
    """PRAGMA readings for one materialised database.

    A torn copy (bare cp of a WAL db, truncated transfer) does NOT read as "integrity != ok":
    sqlite3 raises DatabaseError out of the very first PRAGMA. Letting that propagate turned
    the whole L2 step into reason=helper_crashed -- no L2_FAIL line, no L2_RESULT line, and
    the caller aborts with a bash-level complaint instead of "this baseline's ledger is
    broken" (plan 9 R2: the damage looks normal-sized and opens fine; only the check knows).
    So every sqlite3 error is captured and reported as this db's integrity reading, which the
    caller already treats as a named finding."""
    res = {"integrity": "unreadable", "fk_clean": 0, "rows": {}, "table_absent": [],
           "fk_violations": -1}
    conn = None
    try:
        conn = sqlite3.connect("file:%s?mode=ro" % db_path, uri=True)
        row = conn.execute("PRAGMA integrity_check;").fetchone()
        res["integrity"] = str(row[0]) if row else "no_row"
        fk = conn.execute("PRAGMA foreign_key_check;").fetchall()
        res["fk_clean"] = 1 if not fk else 0
        res["fk_violations"] = len(fk)
        existing = {r[0] for r in conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table';").fetchall()}
        for table in tables:
            if table not in existing:
                res["table_absent"].append(table)
                continue
            res["rows"][table] = int(conn.execute(
                "SELECT COUNT(*) FROM \"%s\";" % table.replace('"', "")).fetchone()[0])
    except sqlite3.Error as exc:
        # ASCII-sanitised: the finding line must survive a cp936 console (see file header).
        detail = ("%s:%s" % (type(exc).__name__, str(exc))).encode("ascii", "replace")
        detail = detail.decode("ascii").replace("/", "_").replace(" ", "_")[:80]
        res["integrity"] = "error_%s" % detail
        res["fk_clean"] = 0
    finally:
        if conn is not None:
            try:
                conn.close()
            except sqlite3.Error:
                pass
    return res


def mode_l2(job):
    manifest_path = job.get("manifest")
    if not manifest_path or not os.path.isfile(manifest_path):
        return fail("l2", "missing_manifest")
    doc = load_json(manifest_path)
    tmp = tempfile.mkdtemp(prefix="cold-l2-")
    fails = []
    infos = []
    try:
        # Materialise only what structural checks need: two databases plus a handful of
        # small JSONs. Not the whole baseline (see R1: disk is the scarce resource here).
        need_db = set(L2_DB_ALL)
        need_json = set(L2_APPLIED) | {"auth.json", "config.json"}
        got = {}
        which = {}
        names = volume_order(doc, job)
        # Pass 1: rel -> the volume that HOLDS THE NEWEST COPY. Volumes are ordered oldest
        # first, so for a path present in both a v1 volume and a v2 delta volume the delta
        # wins; extracting the stale copy would silently run L2 against superseded bytes.
        winner = {}
        accounts_seen = set()
        watch_global_seen = set()
        watch_uid_seen = set()
        try:
            for name in names:
                with tarfile.open(resolve_volume(job, name), mode="r:*") as tf:
                    for member in tf.getmembers():
                        if not member.isfile():
                            continue
                        rel = member.name
                        base = os.path.basename(rel)
                        # Distinct PATHS, not member occurrences. In a v2 run the volume set
                        # is v1 volumes + delta volumes on purpose (plan 5 S3b: v1 stays as
                        # the audit chain, newest copy wins), so a path that changed after
                        # the freeze is physically present twice. Counting occurrences would
                        # read "accounts/ has 3 files" against the SNAPSHOT_OK marker's 2 and
                        # fail L2 for a baseline that is correct -- and an L2 that fires on
                        # good baselines is an L2 nobody trusts.
                        if rel.startswith("accounts/"):
                            accounts_seen.add(rel)
                        if rel.startswith("watchlist"):
                            if rel == "watchlist.json":
                                watch_global_seen.add(rel)
                            else:
                                watch_uid_seen.add(rel)
                        if base in need_db and not os.path.dirname(rel):
                            # Only a root-level path is the ledger: accounts/x/live.db or
                            # config_history/live.db would be a coincidental name (plan 4.2).
                            winner[base] = (name, rel, rel)
                        elif base in need_json and not os.path.dirname(rel):
                            winner[base] = (name, rel, rel)
            # Pass 2: extract exactly the winners, one open per volume.
            by_vol = {}
            for base, (name, rel, _which) in winner.items():
                by_vol.setdefault(name, []).append((base, rel))
            for name, wanted in by_vol.items():
                with tarfile.open(resolve_volume(job, name), mode="r:*") as tf:
                    for base, rel in wanted:
                        member = tf.extractfile(rel)
                        if member is None:
                            continue
                        path = os.path.join(tmp, base)
                        with open(path, "wb") as fh:
                            shutil.copyfileobj(member, fh, CHUNK)
                        got[base] = path
                        which[base] = winner[base][2]
        except IOError as exc:
            return fail("l2", "archive_unreadable", "err=%s" % str(exc)[:120])

        for db, tables in sorted(L2_DB_TABLES.items()):
            path = got.get(db)
            if path is None:
                fails.append("db_absent=%s" % db)
                out("L2|db=%s|integrity=absent" % db)
                continue
            r = sqlite_readings(path, tables)
            out("L2|db=%s|src=%s|integrity=%s|fk_clean=%d|rows=%s" % (
                db, which.get(db, db), r["integrity"], r["fk_clean"],
                ",".join("%s:%d" % (k, v) for k, v in sorted(r["rows"].items())) or "-"))
            if r["integrity"] != "ok":
                fails.append("integrity=%s:%s" % (db, r["integrity"]))
            if not r["fk_clean"]:
                fails.append("fk_check=%s:violations=%d" % (db, r.get("fk_violations", -1)))
            for absent in r["table_absent"]:
                fails.append("table_absent=%s:%s" % (db, absent))

        auth = got.get("auth.json")
        doc_auth = None
        if auth is None:
            fails.append("auth_json_absent")
        else:
            try:
                doc_auth = load_json(auth)
            except (ValueError, OSError, UnicodeDecodeError) as exc:
                # Same shape as the torn db: an unreadable identity file is a FINDING.
                # auth.json is the only container of user configuration (plan 3.1 / 4.1),
                # so a crash here would leave the run reporting a bash-level error while the
                # real story is "the P0 identity file in this baseline cannot be parsed".
                fails.append("auth_json_unreadable=%s" % type(exc).__name__)
                out("L2_AUTH|unreadable=%s" % type(exc).__name__)
                doc_auth = None
        if doc_auth is not None:
            counts = []
            for key in L2_AUTH_KEYS:
                value = doc_auth.get(key)
                counts.append("%s=%d" % (key, len(value) if isinstance(value, list) else 0))
            counts.append("schema_version=%s" % doc_auth.get("schema_version", "absent"))
            out("L2_AUTH|" + "|".join(counts))
        # L2_APPLIED is plan 4.3's four-item assumption. On 2026-11-07 the real Guangzhou
        # box only ever had applied_factors.json / applied_patterns.json; applied_rules.json
        # and grayscale_rules.json were absent at the source (read-only verified, not lost
        # in transfer). Keep the strict four-item check by default; a baseline may register
        # an explicit allowance in its header, which must be echoed here so the exception
        # is never silent. The allowance travels with THAT manifest only, so a future box
        # that does produce these files still gets checked.
        allowed = set(x for x in str(job.get("applied_absent_allow", "")).split(",") if x)
        for name in L2_APPLIED:
            if name not in got:
                if name in allowed:
                    out("L2_APPLIED_ABSENT_ALLOWED|name=%s" % name)
                else:
                    fails.append("applied_absent=%s" % name)
        accounts_n = len(accounts_seen)
        watch_global_n = len(watch_global_seen)
        watch_uid_n = len(watch_uid_seen)
        out("L2_ACCOUNTS|files=%d" % accounts_n)
        marker = job.get("snapshot_ok")
        if marker and os.path.isfile(marker):
            mark = load_json(marker)
            want = mark.get("accounts_files")
            if want is None:
                out("L2_ACCOUNTS_CROSSCHECK=skipped_no_field")
            elif int(want) != accounts_n:
                fails.append("accounts_files_marker=%s_archive=%d" % (want, accounts_n))
            else:
                out("L2_ACCOUNTS_CROSSCHECK=OK marker=%d archive=%d" % (int(want), accounts_n))
        else:
            out("L2_ACCOUNTS_CROSSCHECK=skipped_no_marker")
        if not watch_global_n:
            infos.append("watchlist_global_absent")
        out("L2_WATCHLIST|global=%d|per_uid=%d" % (watch_global_n, watch_uid_n))
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    for line in infos:
        out("L2_WARN|%s" % line)
    if fails:
        for f in fails[:50]:
            out("L2_FAIL|%s" % f)
        out("L2_RESULT=FAIL fails=%d" % len(fails))
        return 1
    out("L2_RESULT=OK fails=0")
    return ok("l2", "fails=0")


# -- mode: scan (S3b incremental) ---------------------------------------------------
def mode_scan(job):
    work = job.get("work")
    manifest_path = job.get("manifest")
    since = int(job.get("since", 0))
    if not work or not manifest_path or not os.path.isfile(manifest_path):
        return fail("scan", "missing_manifest")
    doc = load_json(manifest_path)
    base = {e["rel_path"]: e for e in doc["files"]}
    deleted_known = set(doc.get("deleted", []))
    entries, excluded, _junk = [], [], []
    for root in job.get("roots", []):
        found, ex, junk = walk_root(root)
        if found is None:
            return fail("scan", "root_absent", "path=%s" % root.get("path"))
        entries.extend(found)
        excluded.extend(ex)
        for line in junk[:10]:
            out("SCAN_NOTE|%s" % line)
    current = {e["rel_path"]: e for e in entries}
    changed, added, config_hits = [], [], []
    for rel, want in sorted(base.items()):
        got = current.get(rel)
        if got is None:
            continue
        content_only = got.get("content_only", False)
        if got["sha256"] != want["sha256"] or (not content_only and int(got["mtime"]) > since):
            changed.append(got)
    for rel, got in sorted(current.items()):
        if rel in base or rel in deleted_known:
            continue
        added.append(got)
    for e in changed + added:
        if any(fnmatch.fnmatch(e["rel_path"], pat) for pat in CONFIG_GLOBS):
            config_hits.append(e["rel_path"])
    deleted = [rel for rel in sorted(base) if rel not in current]
    delta = {"tag": "delta", "hashed": True, "entries": changed + added,
             "excluded": excluded,
             "totals": {"files": len(changed) + len(added),
                        "bytes": sum(int(e["bytes"]) for e in changed + added),
                        "accounts_files": accounts_file_count(changed + added)}}
    dump_json(os.path.join(work, "enum_delta.json"), delta)
    meta = {"changed": [e["rel_path"] for e in changed], "added": [e["rel_path"] for e in added],
            "deleted": deleted, "config_paths": config_hits, "since": since}
    dump_json(os.path.join(work, "delta_meta.json"), meta)
    for rel in config_hits:
        out("SCAN_CONFIG_TOUCHED|%s" % rel)
    for rel in deleted[:50]:
        out("SCAN_DELETED|%s" % rel)
    out("SCAN_SUMMARY|changed=%d|added=%d|deleted=%d|config=%d" % (
        len(changed), len(added), len(deleted), len(config_hits)))
    if config_hits and not str(job.get("config_registered", "")).strip():
        # plan 5 S3b: a config change after frozen_at is not ordinary data drift. Identity
        # files (auth.json / config.json / config_history / accounts rules) decide what the
        # new box will actually do, so the operator must state, in the run, what changed and
        # why it is expected -- "close enough" is the thing the doc forbids. The delta JSON
        # is still on disk for inspection; only the v2 seal is refused.
        out("SCAN_REFUSE|reason=config_change_after_frozen_at|paths=%d" % len(config_hits))
        return fail("scan", "config_change_unregistered",
                    "rerun_with=--config-change-registered")
    return ok("scan", "delta=%d" % (len(changed) + len(added)))


# -- mode: merge (v2 file set = v1 minus deleted/changed plus delta) ----------------
def mode_merge(job):
    work = job.get("work")
    if not work:
        return fail("merge", "bad_job")
    try:
        doc = load_json(job["manifest"])
        delta = load_json(job["delta_enum"])
    except (OSError, KeyError, ValueError):
        return fail("merge", "merge_input_bad")
    take = {e["rel_path"] for e in delta["entries"]}
    deleted = set(job.get("deleted", []))
    kept = [e for e in doc["files"] if e["rel_path"] not in take and e["rel_path"] not in deleted]
    merged = kept + [dict(e, abs=e.get("abs", "")) for e in delta["entries"]]
    seen = set()
    for e in merged:
        if e["rel_path"] in seen:
            return fail("merge", "dup_rel_path", "rel=%s" % e["rel_path"])
        seen.add(e["rel_path"])
    payload = {"tag": "merged", "hashed": True, "entries": sorted(merged, key=lambda x: x["rel_path"]),
               "excluded": doc.get("excluded", []) + delta.get("excluded", []),
               "totals": {"files": len(merged),
                          "bytes": sum(int(e["bytes"]) for e in merged),
                          "accounts_files": accounts_file_count(merged)}}
    path = os.path.join(work, "enum_merged.json")
    dump_json(path, payload)
    # Volumes: v2 references the v1 volumes PLUS the delta ones (the delta volumes are
    # numbered from a later index so they sort last, which is what makes "newest copy wins"
    # in verify/l2 come out of the ordering rather than out of a name convention).
    volume_rows = []
    for extra in job.get("volumes_files", []):
        try:
            volume_rows.extend(load_json(extra)["volumes"])
        except (OSError, KeyError, ValueError):
            return fail("merge", "merge_input_bad", "volumes_file=%s" % extra)
    if volume_rows:
        dump_json(os.path.join(work, "volumes_merged.json"),
                  {"volumes": sorted(volume_rows, key=lambda r: (int(r.get("seq", 0)), r["name"])),
                   "total_bytes": sum(int(r["bytes"]) for r in volume_rows)})
        out("MERGE_VOLUMES|files=%d" % len(volume_rows))
    # The B-class containers were collected during the FULL run; an increment must carry
    # them forward instead of silently publishing secrets=[] for v2 (which would read as
    # "this baseline has no keys" -- the opposite of the truth, and unrecoverable later).
    dump_json(os.path.join(work, "secrets_merged.json"),
              {"secrets": doc.get("secrets", [])})
    out("MERGE_SECRETS|entries=%d" % len(doc.get("secrets", [])))
    out("MERGE_RESULT=OK kept=%d delta=%d total=%d out=%s" % (
        len(kept), len(delta["entries"]), len(merged), path))
    return ok("merge", "total=%d" % len(merged))


# -- mode: hash (one file, for verify-on-arrival) -----------------------------------
# -- mode: spec (line-oriented job builder; the bash side never has to write JSON) ---
# Why this exists: the job is handed to the remote python as ONE base64 argument, because
# an argv list containing '*' or '=' is exactly what breaks when it passes through cmd.exe
# or PowerShell on the way (single quotes are not quoting characters in cmd). The bash side
# therefore writes a flat spec file and asks for the base64 back -- no nested quoting, no
# 8 KB command line, no JSON escaping in shell.
SPEC_INT_KEYS = ("version", "since", "index_start", "volume_max_bytes",
                 "big_file_min_bytes", "check_excluded")
SPEC_BOOL_KEYS = ("compress", "keep_records", "plan_only")
# spec key -> job key for the repeatable lines (a spec line appends to the job list).
SPEC_LIST_MAP = {"enum_file": "enum_files", "deleted": "deleted",
                 "extra_volume": "extra_volume_names", "secret": "secret_rows",
                 "vpath": "volume_path_rows", "volumes_file": "volumes_files"}


def parse_spec(lines):
    """Flat key=value spec -> job dict. Repeated keys append to a list; 'root' starts a new
    source root and 'exclude'/'only' attach to the root above them, which keeps the spec
    readable without any nesting the shell would have to escape."""
    job = {}
    roots = []
    for raw in lines:
        line = raw.rstrip("\n").rstrip("\r")
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if "=" not in line:
            raise ValueError("spec_line_without_equals:" + line[:60])
        key, value = line.split("=", 1)
        key = key.strip()
        if key == "root":
            parts = value.split("|")
            if not parts or not parts[0]:
                raise ValueError("spec_root_empty")
            roots.append({"path": parts[0],
                          "prefix": (parts[1] if len(parts) > 1 else ""),
                          "group": (parts[2] if len(parts) > 2 and parts[2] else "data"),
                          "hash": (parts[3] not in ("0", "false", "no")
                                   if len(parts) > 3 and parts[3] else True),
                          # 5th field: content_only=1 means "an mtime newer than the freeze is
                          # NOT evidence of a change". The db snapshot roots need it: their
                          # mtime is when WE took the snapshot, so every increment would
                          # otherwise re-transfer both ledgers and call that an "update".
                          "content_only": (len(parts) > 4 and parts[4] in ("1", "true", "yes")),
                          "exclude": [], "only": []})
            job["roots"] = roots
            continue
        if key in ("exclude", "only"):
            if not roots:
                raise ValueError("spec_%s_without_root" % key)
            if key == "exclude":
                parts = value.split("|")
                if len(parts) != 2 or not parts[0] or not parts[1]:
                    # An exclusion without a reason category is refused here, at the only
                    # place that can still tell the operator: plan 4.7 makes it mandatory.
                    raise ValueError("spec_exclude_needs_reason:" + value[:60])
                roots[-1]["exclude"].append([parts[0], parts[1]])
            else:
                roots[-1]["only"].append(value)
            continue
        if key.startswith("header."):
            job.setdefault("header", {})[key[len("header."):]] = value
            continue
        if key in SPEC_LIST_MAP:
            job.setdefault(SPEC_LIST_MAP[key], []).append(value)
            continue
        if key in SPEC_BOOL_KEYS:
            job[key] = value in ("1", "true", "yes")
            continue
        if key in SPEC_INT_KEYS:
            job[key] = int(value)
            continue
        job[key] = value
    rows = job.pop("secret_rows", [])
    secrets = []
    for item in rows:
        parts = item.split("|")
        if len(parts) < 5:
            raise ValueError("secret_line_needs_path_class_container_sha_bytes")
        # 6th field (optional): the container's octal mode, recorded so the restore side can
        # check "owner-only readable" against the manifest instead of against whatever the
        # seal step left on disk (plan 4.6: mode 0600 is part of the B-class contract).
        secrets.append({"path": parts[0], "class": parts[1], "container": parts[2],
                        "container_sha256": parts[3], "container_bytes": int(parts[4]),
                        "container_mode": (parts[5] if len(parts) > 5 else "")})
    if secrets:
        job["secrets"] = {"secrets": secrets}
    paths = {}
    for item in job.pop("volume_path_rows", []):
        parts = item.split("|")
        if len(parts) != 2 or not parts[0] or not parts[1]:
            raise ValueError("vpath_line_needs_name_and_path")
        paths[parts[0]] = parts[1]
    if paths:
        job["volume_paths"] = paths
    return job


def mode_spec(job):
    path = job.get("spec")
    if not path or not os.path.isfile(path):
        return fail("spec", "spec_absent", "path=%s" % path)
    try:
        with io.open(path, encoding="utf-8") as fh:
            built = parse_spec(fh.readlines())
    except ValueError as exc:
        return fail("spec", "bad_spec", "err=%s" % str(exc)[:160])
    blob = base64.b64encode(json.dumps(built, sort_keys=True).encode("utf-8")).decode("ascii")
    out("JOB_B64|%s|mode=%s" % (blob, built.get("mode", "-")))
    return ok("spec", "keys=%d" % len(built))


def mode_hash(job):
    path = job.get("path")
    if not path or not os.path.isfile(path):
        return fail("hash", "target_absent", "path=%s" % path)
    out("H|path=%s|bytes=%d|sha256=%s" % (path, os.path.getsize(path), sha256_path(path)))
    return ok("hash")


# -- mode: selfcheck (used by the shell test to prove the ruler itself has teeth) ---
def mode_selfcheck(job):
    digest = sha256_bytes(base64.b64decode(job.get("b64", ""))) if job.get("b64") else ""
    out("SELFCHECK|sha_of_probe=%s" % digest[:16])
    return ok("selfcheck")


MODES = {"enumerate": mode_enumerate, "build": mode_build, "manifest": mode_manifest,
         "verify": mode_verify, "l2": mode_l2, "scan": mode_scan, "merge": mode_merge,
         "hash": mode_hash, "selfcheck": mode_selfcheck, "spec": mode_spec}


def main(argv):
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--job", default="")
    parser.add_argument("--spec", default="")
    parser.add_argument("--mode", default="")
    parser.add_argument("--file", default="")
    parser.add_argument("--work", default="")
    args, _unknown = parser.parse_known_args(argv)
    job = None
    if args.job:
        try:
            job = json.loads(base64.b64decode(args.job.encode("ascii")).decode("ascii"))
        except (binascii.Error, UnicodeDecodeError, ValueError):
            return fail("job", "bad_job")
    elif args.spec:
        # Local shape: read the spec and run the mode it names, no base64 round trip.
        # "--mode=spec --spec=FILE" turns the same spec into the base64 job for the remote
        # shape; the parsing is shared, so the two shapes cannot drift apart.
        if args.mode == "spec":
            job = {"mode": "spec", "spec": args.spec}
        else:
            try:
                with io.open(args.spec, encoding="utf-8") as fh:
                    job = parse_spec(fh.readlines())
            except (OSError, ValueError) as exc:
                return fail("spec", "bad_spec", "err=%s" % str(exc)[:160])
            if args.mode and job.get("mode") != args.mode:
                return fail("spec", "spec_mode_mismatch",
                            "spec=%s asked=%s" % (job.get("mode"), args.mode))
    else:
        job = {"mode": args.mode, "path": args.file, "work": args.work}
    mode = job.get("mode", "")
    handler = MODES.get(mode)
    if handler is None:
        return fail(mode or "unknown", "unknown_mode")
    try:
        return handler(job)
    except SystemExit:
        raise
    except Exception as exc:  # noqa: BLE001 - an unexpected raise must still be a named line
        return fail(mode, "helper_crashed",
                    "err=%s:%s" % (type(exc).__name__, str(exc)[:160]))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
