#!/usr/bin/env python3
"""Record a real terminal for the README demo as an asciicast v2 file.

scripts/record-demo.sh starts this script. It runs an interactive Bash in a
pseudo-terminal and records everything the terminal shows. It takes one
instruction per line from the control FIFO:

    line TEXT    type TEXT, press Enter and wait for the next prompt
    keys TEXT    type TEXT; backslash escapes such as \\t, \\r and \\x1b work
    expect TEXT  wait until the terminal shows TEXT after the last keys
    mark         answer with the seconds since the recording started
    stop         end the recording and write the cast

After the first prompt and after each instruction, it writes one line to the
ack FIFO: "ok", the seconds for mark, and for the first prompt the Unix time
at which the recording starts.

The URL fragment of the browser view is a credential. The cast replaces its
characters with dots, so the fragment never reaches a file.
"""

import argparse
import codecs
import fcntl
import json
import os
import pty
import re
import signal
import struct
import termios
import threading
import time

# The prompt sets this window title; it marks each new prompt in the output.
PROMPT_MARK = "\x1b]0;demo\x07"
FRAGMENT = re.compile(r"#[A-Za-z0-9_-]{43}")
TYPING_DELAY = 0.02


class Terminal:
    def __init__(self, cols, rows, shell):
        self.events = []  # (seconds since start, text)
        self.text = ""
        self.since = 0  # where the output after the last keys starts
        self.lock = threading.Lock()
        self.changed = threading.Condition(self.lock)
        self.start = None
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.execvp(shell[0], shell)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        threading.Thread(target=self.read, daemon=True).start()

    def read(self):
        decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
        while True:
            try:
                data = os.read(self.fd, 65536)
            except OSError:
                data = b""
            with self.lock:
                if not data:
                    self.changed.notify_all()
                    return
                chunk = decoder.decode(data)
                now = 0.0 if self.start is None else time.time() - self.start
                self.events.append((now, chunk))
                self.text += chunk
                self.changed.notify_all()

    def prompts(self):
        with self.lock:
            return self.text.count(PROMPT_MARK)

    def wait_prompt(self, count):
        with self.lock:
            while self.text.count(PROMPT_MARK) <= count:
                self.changed.wait()

    def expect(self, text):
        with self.lock:
            while text not in self.text[self.since :]:
                if not self.changed.wait(timeout=30):
                    raise SystemExit(f"demo-terminal: the terminal never showed {text!r}")

    def type(self, text):
        with self.lock:
            self.since = len(self.text)
        for char in text:
            os.write(self.fd, char.encode())
            time.sleep(TYPING_DELAY)

    def begin(self):
        """Start the clock: everything shown so far becomes the first frame."""
        with self.lock:
            self.start = time.time()
            self.events = [(0.0, "".join(text for _, text in self.events))]
            return self.start

    def cast(self, path, cols, rows):
        with self.lock:
            self.events.append((time.time() - self.start, ""))
            events = list(self.events)
        whole = list("".join(text for _, text in events))
        for match in FRAGMENT.finditer("".join(whole)):
            whole[match.start() + 1 : match.end()] = "•" * (match.end() - match.start() - 1)
        with open(path, "w", encoding="utf-8") as out:
            out.write(json.dumps({"version": 2, "width": cols, "height": rows}) + "\n")
            offset = 0
            for at, text in events:
                piece = "".join(whole[offset : offset + len(text)])
                offset += len(text)
                out.write(json.dumps([round(at, 3), "o", piece]) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--control", required=True)
    parser.add_argument("--ack", required=True)
    parser.add_argument("--cast", required=True)
    parser.add_argument("--cols", type=int, default=64)
    parser.add_argument("--rows", type=int, default=32)
    parser.add_argument("shell", nargs="+")
    args = parser.parse_args()

    term = Terminal(args.cols, args.rows, args.shell)
    control = open(args.control, encoding="utf-8")
    ack = open(args.ack, "w", encoding="utf-8")
    term.wait_prompt(0)
    ack.write(f"{term.begin():.3f}\n")
    ack.flush()
    for instruction in control:
        name, _, text = instruction.rstrip("\n").partition(" ")
        seen = term.prompts()
        answer = "ok"
        if name == "line":
            term.type(text + "\r")
            term.wait_prompt(seen)
        elif name == "keys":
            term.type(codecs.decode(text, "unicode_escape"))
        elif name == "expect":
            term.expect(text)
        elif name == "mark":
            answer = f"{time.time() - term.start:.2f}"
        elif name == "stop":
            term.cast(args.cast, args.cols, args.rows)
            os.kill(term.pid, signal.SIGHUP)
        else:
            raise SystemExit(f"demo-terminal: unknown instruction {name!r}")
        ack.write(answer + "\n")
        ack.flush()
        if name == "stop":
            return


if __name__ == "__main__":
    main()
