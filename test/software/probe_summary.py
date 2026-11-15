# SPDX-License-Identifier: AGPL-3.0-only
#
# Prints a probe answer (POST /api/v1/software/probe) one slot per line and
# exits non-zero when a slot failed, a runtime download has no usable
# source, or the source requested for it is not one of the usable ones.
#
#   python3 probe_summary.py probe.json [software@line[#source]...]
import json
import sys

answer = json.load(open(sys.argv[1]))
# Requested sources by runtime slot ID.
want = {}
for entry in sys.argv[2:]:
    item, _, src = entry.partition("#")
    if src:
        sw, _, line = item.partition("@")
        want["runtime:%s:%s" % (sw, line)] = src
slots = answer.get("slots") or []
if not slots:
    sys.exit("no slots")


def cand(c):
    speed = c.get("speed") or 0
    lag = c.get("lagSeconds") or 0
    return "%s%s:%s%s%s" % (
        "*" if c.get("inUse") else "",
        c["id"],
        c["status"],
        "(-%dh)" % (lag // 3600) if lag else "",
        "@%.1fMB/s" % (speed / 1048576) if speed else "",
    )


ok = True
for s in slots:
    if s.get("error"):
        print("    probe %s: error %s" % (s["id"], s["error"]))
        ok = False
        continue
    shown = [c for c in s["candidates"] if c["status"] != "skipped"]
    skipped = len(s["candidates"]) - len(shown)
    print("    probe %s -> %s%s [%s]%s" % (
        s["id"],
        s.get("selected", ""),
        " warning=" + s["warning"] if s.get("warning") else "",
        " ".join(cand(c) for c in shown),
        " +%d not compared" % skipped if skipped else "",
    ))
    if s["kind"] == "runtime":
        usable = [c["id"] for c in s["candidates"] if c["eligible"]]
        if not usable or not s.get("size"):
            ok = False
        elif s["id"] in want and want[s["id"]] not in usable:
            print("    requested source %s is not usable" % want[s["id"]])
            ok = False
est = answer.get("packages")
if est:
    print("    package download: %s" % (
        "%.1f MB in %d packages%s" % (est["bytes"] / 1048576, est["packages"], ", partial" if est.get("partial") else "")
        if est["known"] else "unknown"))
print("    probe took %d ms" % answer.get("took", 0))
sys.exit(0 if ok else 1)
