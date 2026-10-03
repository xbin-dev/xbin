#!/usr/bin/env python3
# hack/demo/clock.py — the film set's clock (README.md §Resetting between
# takes): one time zone, one "now" and one day for everything on the set —
# the seed's fixtures, the tiles' times of day, git's dates, xbind and the
# scripted model (hack/fakeopenai -day). Prints shell assignments:
#
#   DEMO_TZ=America/New_York   the set's zone: $DEMO_TZ, else company.json's
#                              company.timeZone (the company's own)
#   NOW_MS=1759437600000       the set's now: the real now on a weekday between
#                              08:00 and 19:00 there, else the last working
#                              day's 16:40 — so a set seeded at night or on a
#                              weekend still reads like a working afternoon,
#                              never one in the future
#   DEMO_DAY=2026-10-02        the date of NOW_MS: "today" in every fixture,
#                              the calendar's day and the model's {{weekday:0}}
#
# NOW_MS and DEMO_DAY already in the environment are kept (up.sh computes
# them once and hands them on); a NOW_MS alone gets its own date.
import datetime as dt
import json
import os
import sys
from zoneinfo import ZoneInfo

here = os.path.dirname(os.path.abspath(__file__))
tz = os.environ.get("DEMO_TZ") or json.load(open(os.path.join(here, "company.json")))["company"].get("timeZone") or "UTC"
try:
    zone = ZoneInfo(tz)
except Exception as e:  # a typo'd DEMO_TZ is a mistake, not UTC
    sys.exit(f"clock.py: unknown time zone {tz!r}: {e}")

if os.environ.get("NOW_MS"):
    now = dt.datetime.fromtimestamp(int(os.environ["NOW_MS"]) / 1000, zone)
else:
    n = dt.datetime.now(zone)
    if n.weekday() < 5 and 8 <= n.hour < 19:
        now = n.replace(microsecond=0)
    else:
        d = n if n.hour >= 19 else n - dt.timedelta(days=1)
        while d.weekday() >= 5:
            d -= dt.timedelta(days=1)
        now = d.replace(hour=16, minute=40, second=0, microsecond=0)
day = os.environ.get("DEMO_DAY") or now.date().isoformat()

print(f"DEMO_TZ={tz}")
print(f"NOW_MS={int(now.timestamp() * 1000)}")
print(f"DEMO_DAY={day}")
