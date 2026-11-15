# SPDX-License-Identifier: AGPL-3.0-only
#
# Prints a probe answer (POST /api/v1/software/probe) one slot per line and
# exits non-zero when a slot failed, a runtime download has no usable
# source, or the requested source is not one of the usable ones.
#
#   python3 probe_summary.py probe.json [requested-source]
import json
import sys

answer = json.load(open(sys.argv[1]))
want = sys.argv[2] if len(sys.argv) > 2 else ""
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
        if not usable:
            ok = False
        elif want and want not in usable:
            print("    requested source %s is not usable" % want)
            ok = False
print("    probe took %d ms" % answer.get("took", 0))
sys.exit(0 if ok else 1)
