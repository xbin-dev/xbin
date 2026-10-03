#!/usr/bin/env python3
"""Record a real terminal session byte for byte, for xbin.dev's brand-terminal shots
(S-4, S-5, S-11, S-12; website/README.md → "Assets", website/shots.todo.md).

    hack/website-terminal-record.py --out website/art/shots/S-4.session [--steps STEPS.json] \\
        [--cols 97 --rows 24] -- CMD...

CMD runs on a pseudo-terminal of that size: an interactive shell with the prompt "$ ",
or ssh -tt into a fresh machine. The steps (STEPS.json, or the "steps" of OUT.json, where
each shot keeps them) type into it the way a person does and wait for what the screen
says:

    {"wait": REGEX, "timeout": S}   until the output since the last wait matches
    {"type": TEXT, "delay": S}      one character at a time
    {"send": TEXT}                  at once (for example "\\r")
    {"mark": NAME}                  remember the byte offset ("cmd": typing starts,
                                    "end": the moment the shot shows)
    {"sleep": S}

Everything CMD writes is kept, unedited, in OUT; OUT.json holds the marks and the size.
hack/website-terminal.mjs replays OUT from the last screen clear before "cmd" to "end".
"""
import argparse
import fcntl
import json
import os
import pty
import re
import select
import struct
import sys
import termios
import time

ap = argparse.ArgumentParser()
ap.add_argument('--steps')
ap.add_argument('--out', required=True)
ap.add_argument('--cols', type=int, default=97)
ap.add_argument('--rows', type=int, default=24)
ap.add_argument('cmd', nargs=argparse.REMAINDER)
a = ap.parse_args()
cmd = a.cmd[1:] if a.cmd and a.cmd[0] == '--' else a.cmd
if not cmd:
    sys.exit('website-terminal-record: no command to run (put it after --)')
meta = {}
if os.path.exists(a.out + '.json'):
    with open(a.out + '.json') as f:
        meta = json.load(f)
if a.steps:
    with open(a.steps) as f:
        meta['steps'] = json.load(f)
steps = meta.get('steps')
if not steps:
    sys.exit(f'website-terminal-record: no steps (--steps, or "steps" in {a.out}.json)')

pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', a.rows, a.cols, 0, 0))

buf = bytearray()
seen = 0  # where the last wait's match ended
marks = {}


def pump(t):
    r, _, _ = select.select([fd], [], [], t)
    if r:
        try:
            d = os.read(fd, 65536)
        except OSError:
            return
        buf.extend(d)


def drain(s):
    end = time.time() + s
    while time.time() < end:
        pump(max(0, end - time.time()))


for st in steps:
    if 'wait' in st:
        rx = re.compile(st['wait'].encode(), re.S)
        end = time.time() + st.get('timeout', 30)
        while True:
            m = rx.search(bytes(buf), seen)
            if m:
                seen = m.end()
                break
            if time.time() > end:
                tail = bytes(buf[-1500:]).decode(errors='replace')
                sys.exit(f'website-terminal-record: timed out waiting for {st["wait"]!r}; the last output:\n{tail}')
            pump(0.1)
    elif 'type' in st:
        for ch in st['type']:
            os.write(fd, ch.encode())
            drain(st.get('delay', 0.04))
    elif 'send' in st:
        os.write(fd, st['send'].encode())
    elif 'mark' in st:
        drain(0.05)
        marks[st['mark']] = len(buf)
    elif 'sleep' in st:
        drain(st['sleep'])

drain(0.3)
with open(a.out, 'wb') as f:
    f.write(bytes(buf))
# OUT.json keeps what else it says (the steps, the shot's view, where it was captured)
meta.update({'cols': a.cols, 'rows': a.rows, 'marks': marks})
with open(a.out + '.json', 'w') as f:
    json.dump(meta, f, indent=1, ensure_ascii=False)
    f.write('\n')
try:
    os.kill(pid, 9)
except OSError:
    pass
print(f'website-terminal-record: {a.out}, {len(buf)} bytes, marks {marks}')
