#!/usr/bin/env python3
"""Disposable v1 CLI/API validation. Python standard library only; no live Herdr."""

import argparse
import contextlib
import datetime
import hashlib
import http.client
import http.server
import json
import math
import os
from pathlib import Path
import platform
import select
import shlex
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import urlsplit

REPO = Path(__file__).resolve().parent.parent
NOTICE = "a-stack: new Task mail for you. If you are a registered a-stack Agent, run your mail check."
SAMPLES, WARMUP = 20, 2


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def private_json(path, value):
    temporary = path.with_suffix(".new")
    with temporary.open("w", encoding="utf-8") as output:
        os.chmod(temporary, 0o600)
        json.dump(value, output)
    temporary.replace(path)


def fake_herdr(config_path, args):
    config = json.loads(Path(config_path).read_text())
    if args == ["status", "server", "--json"]:
        print(
            json.dumps(
                {
                    "status": "running",
                    "running": True,
                    "version": "0.9.5",
                    "protocol": 22,
                    "compatible": True,
                    "endpoint_compatible": True,
                }
            )
        )
        return 0
    if args[:2] == ["agent", "get"] and len(args) == 3:
        # The notifier asks by alias; herdr bind asks by its own pane.
        target_key = "pane_id" if args[2].startswith("w1:p") else "herdr_alias"
    elif args[:2] == ["agent", "prompt"] and len(args) == 4:
        target_key = "pane_id"
    else:
        return 2
    target = next((b for b in config["targets"] if args[2] == b[target_key]), None)
    if target is None or os.environ.get("HERDR_SOCKET_PATH") != target["endpoint"]:
        print(json.dumps({"error": {"code": "agent_not_found"}}), file=sys.stderr)
        return 1
    if args[1] == "prompt":
        if len(args) != 4 or args[2] != target["pane_id"] or args[3] != NOTICE:
            return 2
        # Only this fixed generic instruction and synthetic IDs enter the log.
        event = {
            "event": "prompt",
            "pane": args[2],
            "monotonic_ns": time.monotonic_ns(),
        }
        with Path(config["log"]).open("a") as log:
            log.write(json.dumps(event) + "\n")
        if config["mode"] == "crash":
            os.kill(os.getppid(), signal.SIGKILL)
            return 0
        if config["mode"] == "uncertain":
            print(json.dumps({"error": {"code": "timeout"}}), file=sys.stderr)
            return 1
    agent = {
        "name": target["herdr_alias"],
        "pane_id": target["pane_id"],
        "terminal_id": target["terminal_id"],
        "agent": "codex",
        "agent_status": config["status"],
        "agent_session": {
            "agent": "codex",
            "kind": "id",
            "source": "herdr:codex",
            "value": target["native_session_id"],
        },
    }
    print(json.dumps({"result": {"agent": agent}}))
    return 0


class Run:
    def __init__(self, binary, root):
        self.binary, self.root = binary, root
        self.state = root / "state"
        self.credentials = root / "credentials"
        self.credentials.mkdir(mode=0o700)
        self.project = root / "project"
        self.project.mkdir()
        (self.project / ".git").mkdir()
        self.backups = root / "backups"
        self.backups.mkdir(mode=0o700)
        self.config_path, self.log = root / "fake.json", root / "fake.jsonl"
        self.config = {
            "targets": [],
            "status": "working",
            "mode": "",
            "log": str(self.log),
        }
        self.fake = root / "fake-herdr"
        self.fake.write_text(
            "#!/bin/sh\nexec "
            + shlex.join(
                [
                    sys.executable,
                    str(Path(__file__).resolve()),
                    "--fake-herdr",
                    str(self.config_path),
                ]
            )
            + ' "$@"\n'
        )
        self.fake.chmod(0o700)
        self.seq = self.calls = self.api_calls = 0
        self.process = self.connection = None
        self.ref = {}
        self.stage = "startup"
        self.set_fake()

    def op(self):
        self.seq += 1
        return f"validation-{self.seq:08d}"

    @property
    def context(self):
        return [
            "--workspace",
            self.ref["workspace_id"],
            "--session",
            self.ref["session_id"],
        ]

    def set_fake(self, **changes):
        self.config.update(changes)
        private_json(self.config_path, self.config)

    def start(self, notices=False, missing=False):
        if self.connection:
            self.connection.close()
            self.connection = None
        command = [str(self.binary), "serve", "--state-dir", str(self.state), "--json"]
        if notices:
            command += [
                "--herdr-executable",
                str(self.root / "absent-herdr") if missing else str(self.fake),
            ]
        self.process = subprocess.Popen(
            command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL
        )
        deadline, ready = time.monotonic() + 10, bytearray()
        while b"\n" not in ready:
            remaining = deadline - time.monotonic()
            check(
                remaining > 0
                and select.select([self.process.stdout], [], [], remaining)[0],
                "service readiness timeout",
            )
            chunk = os.read(self.process.stdout.fileno(), 4097 - len(ready))
            check(chunk, "service closed before complete readiness")
            ready.extend(chunk)
            check(len(ready) <= 4096, "service readiness exceeds limit")
        line = ready.split(b"\n", 1)[0]
        check(json.loads(line)["data"]["status"] == "ready", "service failed to start")
        self.discovery = json.loads((self.state / "service.json").read_text())
        self.address = urlsplit(self.discovery["url"])

    def stop(self, kill=False):
        process, self.process = self.process, None
        failure = None
        try:
            if self.connection:
                self.connection.close()
                self.connection = None
            if process and process.poll() is None:
                process.kill() if kill else process.terminate()
                try:
                    code = process.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
                    failure = "service shutdown timed out; owned child killed/reaped"
                else:
                    if not kill and code != 0:
                        failure = "service shutdown failed"
        finally:
            if process and process.stdout:
                process.stdout.close()
        check(failure is None, failure)

    def cli(
        self,
        *args,
        actor=None,
        expect=None,
        stdin=None,
        state=None,
        ambiguous=False,
        env=None,
    ):
        command = [
            str(self.binary),
            *map(str, args),
            "--state-dir",
            str(state or self.state),
            "--json",
        ]
        if actor:
            command += [
                "--as",
                actor.get("kind", "agent"),
                "--credential-file",
                str(actor["file"]),
            ]
        self.calls += 1
        result = subprocess.run(
            command,
            input=stdin,
            capture_output=True,
            timeout=35,
            env=os.environ | env if env else None,
        )
        if ambiguous and result.returncode:
            check(
                not result.stdout
                and json.loads(result.stderr)["error"]["code"] == "storage_unavailable",
                "ambiguous call reported false success",
            )
            return None
        if expect:
            check(
                result.returncode != 0 and not result.stdout,
                "rejected CLI operation succeeded",
            )
            error = json.loads(result.stderr)["error"]
            check(
                error["code"] == expect,
                "CLI rejection "
                + " ".join(map(str, args[:2]))
                + ": expected "
                + expect
                + ", got "
                + error["code"],
            )
            return error
        # task create may print one hint line when the new Task has no prerequisite.
        quiet = (
            not result.stderr
            or args[:2] == ("task", "create")
            and result.stderr.startswith(b"a-stack: hint: ")
            and result.stderr.count(b"\n") == 1
        )
        check(
            result.returncode == 0 and quiet,
            "CLI operation failed: " + " ".join(map(str, args[:2])),
        )
        return json.loads(result.stdout)["data"]

    def api(self, path, payload=None, actor=None, expect=None, headers=None):
        if not self.connection:
            self.connection = http.client.HTTPConnection(
                self.address.hostname, self.address.port, timeout=15
            )
        credential = json.loads(
            Path(actor["file"] if actor else self.state / "operator.json").read_text()
        )
        body = json.dumps(self.ref | (payload or {})).encode()
        request_headers = {
            "Authorization": "Bearer " + credential["token"],
            "X-A-Stack-Instance": self.discovery["instance_id"],
            "Content-Type": "application/json",
        }
        request_headers.update(headers or {})
        self.connection.request("POST", path, body, request_headers)
        response = self.connection.getresponse()
        data = response.read(1048577)
        self.api_calls += 1
        check(len(data) <= 1048576, "response exceeded one MiB")
        result = json.loads(data) if data else None
        if expect:
            check(
                response.status >= 400 and result["error"]["code"] == expect,
                "API rejection "
                + path
                + ": expected "
                + expect
                + ", got "
                + str(response.status)
                + "/"
                + str((result or {}).get("error", {}).get("code")),
            )
        else:
            check(
                response.status in (200, 304),
                "API request "
                + path
                + ": "
                + str(response.status)
                + "/"
                + str((result or {}).get("error", {}).get("code")),
            )
        return response.status, result, dict(response.getheaders()), len(data)

    def data(self, path, payload=None, actor=None):
        return self.api(path, payload, actor)[1]["data"]

    def session(self):
        return self.data("/v1/session/snapshot")["session"]

    def show(self, task):
        return self.data("/v1/task/show", {"task_id": task})

    def change(self, action, task, actor, *flags):
        return self.cli(
            "task",
            *action.split(),
            *self.context,
            "--task",
            task,
            "--expected-revision",
            self.show(task)["revision"],
            "--operation-id",
            self.op(),
            *flags,
            actor=actor,
        )

    def assign(self, task, actor):
        return self.change("assign", task, self.orch, "--agent", actor["id"])

    def register(self, name, actor=None):
        path = self.credentials / (name + ".json")
        receipt = self.cli(
            "agent",
            "register",
            *self.context,
            "--name",
            name,
            "--new-credential-file",
            path,
            "--operation-id",
            self.op(),
            actor=actor,
        )
        return {"id": receipt["resource_id"], "file": path, "name": name}

    def bootstrap(self, long_name=False):
        self.start()
        workspace = self.cli(
            "workspace", "create", "--path", self.project, "--operation-id", self.op()
        )["resource_id"]
        session = self.cli(
            "session",
            "create",
            "--workspace",
            workspace,
            "--name",
            "Validation",
            "--operation-id",
            self.op(),
        )["resource_id"]
        self.ref = {"workspace_id": workspace, "session_id": session}
        self.orch = self.register("01-orchestrator")
        self.cli(
            "session",
            "appoint",
            *self.context,
            "--agent",
            self.orch["id"],
            "--expected-revision",
            1,
            "--operation-id",
            self.op(),
        )
        self.a = self.register(
            ("02-" + "a" * 61) if long_name else "02-worker", self.orch
        )
        self.b = self.register("03-worker", self.orch)

    def task(self, title):
        return self.cli(
            "task",
            "create",
            *self.context,
            "--title",
            title,
            "--operation-id",
            self.op(),
            actor=self.orch,
        )["resource_id"]

    def edge(self, prior, dependent, expect=None, actor=None):
        return self.cli(
            "dependency",
            "add",
            *self.context,
            "--from",
            prior,
            "--to",
            dependent,
            "--expected-revision",
            self.session()["graph_revision"],
            "--operation-id",
            self.op(),
            actor=actor or self.orch,
            expect=expect,
        )

    def mail(self, task, text, actor=None, operation=None, ambiguous=False):
        return self.cli(
            "mail",
            "send",
            *self.context,
            "--task",
            task,
            "--body-file",
            "-",
            "--operation-id",
            operation or self.op(),
            actor=actor or self.a,
            stdin=text.encode(),
            ambiguous=ambiguous,
        )

    def read(self, task, actor=None, cursor=""):
        return self.cli(
            "mail",
            "read",
            *self.context,
            "--task",
            task,
            "--cursor",
            cursor,
            actor=actor or self.a,
        )

    def pages(self, kind, task=None, actor=None):
        revision = self.session()["read_revision"]
        after, items, cursors = "", [], set()
        while True:
            payload = {
                "kind": kind,
                "read_revision": revision,
                "limit": 100,
                "after": after,
            }
            if task:
                payload["task_id"] = task
            page = self.data("/v1/session/page", payload, actor)
            check(
                page["read_revision"] == revision,
                "snapshot revision changed during collection",
            )
            items.extend(page["items"])
            next_cursor = page.get("next", "")
            if not next_cursor:
                return items
            check(
                page["items"] and next_cursor != after and next_cursor not in cursors,
                "page continuation made no progress",
            )
            cursors.add(next_cursor)
            after = next_cursor

    def binding(self, actor, suffix, rebind=False):
        binding = {
            "notices": "pane",
            "instance_id": "fake-instance-" + suffix,
            "herdr_alias": "fake-" + suffix,
            "endpoint": str(self.root / "fake.sock"),
            "pane_id": "w1:p" + suffix,
            "terminal_id": "fake-terminal-" + suffix,
            "native_session_id": "fake-native-" + suffix,
            "harness": "codex",
            "harness_version": "0.164.0",
        }
        path = self.root / ("binding-" + suffix + ".json")
        private_json(path, binding)
        self.config["targets"] = [
            b
            for b in self.config["targets"]
            if b["herdr_alias"] != binding["herdr_alias"]
        ]
        self.config["targets"].append(binding)
        self.set_fake()
        revision = next(
            a["revision"]
            for a in self.cli("agent", "list", *self.context)["items"]
            if a["id"] == actor["id"]
        )
        if rebind:
            replacement = self.credentials / ("replacement-" + suffix + ".json")
            self.cli(
                "agent",
                "rebind",
                *self.context,
                "--agent",
                actor["id"],
                "--runtime-file",
                path,
                "--expected-revision",
                revision,
                "--operation-id",
                self.op(),
                "--new-credential-file",
                replacement,
            )
            actor["file"] = replacement
        else:
            # The Agent binds itself from inside its (fake) Herdr pane.
            harness = self.root / "bin" / "codex"
            if not harness.exists():
                harness.parent.mkdir(mode=0o700)
                harness.write_text("#!/bin/sh\necho codex-cli 0.164.0\n")
                harness.chmod(0o700)
            env = {
                "HERDR_ENV": "1",
                "HERDR_PANE_ID": binding["pane_id"],
                "HERDR_SOCKET_PATH": binding["endpoint"],
                "PATH": str(harness.parent) + os.pathsep + os.environ["PATH"],
            }
            receipt = self.cli(
                "herdr",
                "bind",
                *self.context,
                "--notices",
                "pane",
                "--agent",
                actor["id"],
                "--herdr-executable",
                self.fake,
                "--expected-revision",
                revision,
                "--operation-id",
                self.op(),
                actor=actor,
                env=env,
            )
            check(receipt["outcome"] == "attached", "herdr bind did not attach")
        return binding

    def status(self, actor=None):
        return self.cli(
            "notifications",
            "status",
            *self.context,
            *(["--agent", actor["id"]] if actor else []),
        )

    def wait(self, predicate, label, timeout=15):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            if predicate():
                return
            time.sleep(0.02)
        raise AssertionError(label)

    def prompts(self):
        return (
            [json.loads(line) for line in self.log.read_text().splitlines()]
            if self.log.exists()
            else []
        )

    def backup(self, label):
        path = self.backups / (label + "-" + self.op() + ".sqlite")
        self.cli("storage", "backup", "--destination", path)
        return path

    def browser(self):
        # Like app.js, trade a view link's one-time code for a view token. The
        # redeem endpoint ignores the Operator bearer that api() sends. The
        # returned link is a second, unused one.
        code = urlsplit(self.cli("open", *self.context, "--no-browser")["url"]).fragment
        credential = self.credentials / "browser.json"
        private_json(
            credential,
            {
                "kind": "browser",
                "token": self.data("/v1/browser/redeem", {"code": code})["token"],
            },
        )
        return {"kind": "browser", "file": credential}, self.cli(
            "open", *self.context, "--no-browser"
        )["url"]


@contextlib.contextmanager
def dropped_response(run):
    saved = {}

    class Proxy(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            connection = http.client.HTTPConnection(
                run.address.hostname, run.address.port, timeout=10
            )
            headers = dict(self.headers)
            headers["Host"] = run.address.netloc
            connection.request(
                "POST",
                self.path,
                self.rfile.read(int(self.headers["Content-Length"])),
                headers,
            )
            response = connection.getresponse()
            saved["receipt"] = json.loads(response.read())["data"]
            connection.close()
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Proxy)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    client_state = run.root / "proxy-client"
    client_state.mkdir(mode=0o700)
    private_json(
        client_state / "service.json",
        run.discovery | {"url": f"http://127.0.0.1:{server.server_port}"},
    )
    try:
        yield client_state, saved
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def inspect_backup(path):
    with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as database:
        check(
            database.execute("PRAGMA integrity_check").fetchone()[0] == "ok",
            "completed backup integrity failed",
        )
        result = {
            "backup_bytes": path.stat().st_size,
            "analysis_sqlite_version": sqlite3.sqlite_version,
        }
        result["rows"] = {
            table: database.execute("SELECT count(*) FROM " + table).fetchone()[0]
            for table in [
                "tasks",
                "mail_messages",
                "mail_mentions",
                "operation_receipts",
                "notice_batches",
                "notice_batch_refs",
            ]
        }
        result["body_bytes"] = database.execute(
            "SELECT coalesce(sum(length(CAST(body AS BLOB))),0) FROM mail_messages"
        ).fetchone()[0]
        result["notice_states"] = dict(
            database.execute(
                "SELECT notice_state,count(*) FROM mail_mentions GROUP BY notice_state"
            )
        )
        result["objects"] = {
            name: {"pages": pages, "bytes": size}
            for name, pages, size in database.execute(
                "SELECT name,count(*),sum(pgsize) FROM dbstat GROUP BY name"
            )
        }
        return result


def scenario(run):
    run.stage = "cross-slice workflow"
    run.bootstrap()
    root, left, right, shared = [
        run.task(name)
        for name in ["Prerequisite", "Left branch", "Unassigned branch", "Shared work"]
    ]
    for actor in [run.a, run.b]:
        run.assign(shared, actor)
    run.assign(root, run.a)
    run.assign(left, run.a)
    for prior, dependent in [
        (root, left),
        (root, right),
        (left, shared),
        (right, shared),
    ]:
        run.edge(prior, dependent)
    run.edge(shared, root, expect="conflict")
    run.edge(root, root, expect="invalid_input")
    run.edge(left, right, expect="forbidden", actor=run.a)
    run.cli(
        "task",
        "create",
        *run.context,
        "--title",
        "Denied",
        "--operation-id",
        run.op(),
        expect="forbidden",
    )
    run.cli("mail", "read", *run.context, "--task", shared, expect="forbidden")
    run.cli(
        "mail",
        "read",
        *run.context,
        "--task",
        shared,
        actor=run.orch,
        expect="forbidden",
    )
    check(run.show(right)["unassigned"], "Unassigned Task hidden")
    stale = run.show(shared)["revision"]
    run.change("status", shared, run.a, "--progress", "working")
    run.cli(
        "task",
        "status",
        *run.context,
        "--task",
        shared,
        "--expected-revision",
        stale,
        "--progress",
        "finished",
        "--summary",
        "Stale",
        "--operation-id",
        run.op(),
        actor=run.b,
        expect="conflict",
    )
    run.change(
        "attention set", shared, run.a, "--question", "Which option should we use?"
    )
    run.cli(
        "task",
        "attention",
        "set",
        *run.context,
        "--task",
        shared,
        "--expected-revision",
        run.show(shared)["revision"],
        "--question",
        "Competing request",
        "--operation-id",
        run.op(),
        actor=run.b,
        expect="conflict",
    )
    run.change(
        "status",
        shared,
        run.b,
        "--progress",
        "finished",
        "--summary",
        "Whole shared Task complete",
    )
    check(run.show(shared)["needs_you"], "completion cleared Operator question")
    run.change("reopen", shared, run.a, "--reason", "Additional work")
    run.change(
        "status", shared, run.b, "--progress", "cancelled", "--reason", "Reconsider"
    )
    run.change("reopen", shared, run.a, "--reason", "Resume")
    check(
        run.show(shared)["result"] == "Whole shared Task complete", "reopen lost result"
    )
    run.change("attention resolve", shared, run.a, "--note", "Decision recorded")
    run.change(
        "status",
        root,
        run.a,
        "--progress",
        "cancelled",
        "--reason",
        "Prerequisite no longer pursued",
    )
    check(
        run.show(left)["cancelled_prerequisite_count"] == 1
        and run.show(left)["progress"] == "waiting",
        "cancelled prerequisite rewrote dependent",
    )
    run.change(
        "report",
        shared,
        run.a,
        "--report",
        "Literal <img src=x onerror=alert(1)> report",
    )
    run.change(
        "reference add",
        shared,
        run.a,
        "--location",
        "/not-read/report.txt",
        "--reference-revision",
        "abc123",
    )
    run.cli(
        "task",
        "reference",
        "add",
        *run.context,
        "--task",
        shared,
        "--expected-revision",
        run.show(shared)["revision"],
        "--location",
        "javascript:alert(1)",
        "--operation-id",
        run.op(),
        actor=run.a,
        expect="invalid_input",
    )
    first_op, first_body = run.op(), "Shared @03-worker and self @02-worker"
    first = run.mail(shared, first_body, operation=first_op)
    check(
        run.mail(shared, first_body, operation=first_op) == first,
        "retry changed original receipt",
    )
    one, two = run.read(shared), run.read(shared, run.b)
    check(one == two and len(one["messages"]) == 1, "mail consumed by another reader")
    second_op = run.op()
    second = run.mail(shared, "Reply @02-worker", run.b, second_op)
    check(
        run.read(shared, cursor=one["cursor"])["messages"][0]["id"]
        == second["resource_id"],
        "caller cursor skipped reply",
    )
    run.cli(
        "mail",
        "send",
        *run.context,
        "--task",
        shared,
        "--body",
        "Changed",
        "--operation-id",
        first_op,
        actor=run.a,
        expect="operation_conflict",
    )
    wrong = run.read(root)["cursor"]
    run.cli(
        "mail",
        "read",
        *run.context,
        "--task",
        shared,
        "--cursor",
        wrong,
        actor=run.a,
        expect="invalid_input",
    )
    lost_op, lost_body = run.op(), "Ambiguous committed response"
    with dropped_response(run) as (state, saved):
        run.cli(
            "mail",
            "send",
            *run.context,
            "--task",
            shared,
            "--body",
            lost_body,
            "--operation-id",
            lost_op,
            actor=run.a,
            state=state,
            expect="storage_unavailable",
        )
    check(
        run.mail(shared, lost_body, operation=lost_op) == saved["receipt"],
        "lost response retry duplicated/changed effect",
    )
    run.change("unassign", shared, run.orch, "--agent", run.b["id"])
    run.cli(
        "mail", "read", *run.context, "--task", shared, actor=run.b, expect="forbidden"
    )
    run.cli(
        "mail",
        "send",
        *run.context,
        "--task",
        shared,
        "--body",
        "Reply @02-worker",
        "--operation-id",
        second_op,
        actor=run.b,
        expect="forbidden",
    )
    run.mail(shared, "While second worker is away")
    run.assign(shared, run.b)
    check(len(run.read(shared, run.b)["messages"]) == 4, "rejoin lost away history")
    # Runtime absence leaves assignee work usable; role replacement is explicit.
    run.cli(
        "agent",
        "attach",
        *run.context,
        "--agent",
        run.orch["id"],
        "--runtime-id",
        "absent-orchestrator",
        "--expected-revision",
        1,
        "--operation-id",
        run.op(),
        actor=run.orch,
    )
    run.cli(
        "agent",
        "observe",
        *run.context,
        "--agent",
        run.orch["id"],
        "--runtime-id",
        "absent-orchestrator",
        "--runtime-state",
        "stopped",
        "--expected-revision",
        2,
    )
    run.change(
        "report",
        shared,
        run.a,
        "--report",
        "Workers continue while Orchestrator is absent",
    )
    old_orch = run.orch
    run.cli(
        "session",
        "appoint",
        *run.context,
        "--agent",
        run.b["id"],
        "--expected-revision",
        run.session()["revision"],
        "--operation-id",
        run.op(),
    )
    run.orch = run.b
    run.cli(
        "task",
        "create",
        *run.context,
        "--title",
        "Old role denied",
        "--operation-id",
        run.op(),
        actor=old_orch,
        expect="forbidden",
    )
    run.task("New Orchestrator can coordinate")
    run.binding(run.b, "b")
    run.stop()
    run.start(notices=True)
    run.mail(shared, "Fresh eligible @03-worker")
    run.wait(
        lambda: run.status(run.b)["targets"][0]["counts"].get("submitted", 0) == 1,
        "eligible notice not submitted",
    )
    run.stop()
    run.start(notices=True, missing=True)
    check(
        run.status(run.b)["targets"][0]["reason"] == "notice_waiting_mail",
        "restart lost unread notice hold",
    )
    run.read(shared, run.b)
    check(
        run.status(run.b)["targets"][0]["next_attempt_unix_ms"] > 0,
        "own read shortened pane cooldown",
    )
    # Use a fresh disposable runtime for the delivery-failure checks.
    run.binding(run.b, "b-next", rebind=True)
    run.mail(shared, "Mail works without integration @03-worker")
    run.wait(
        lambda: run.status(run.b)["targets"][0]["reason"] == "herdr_unavailable",
        "missing integration lacks diagnostic",
    )
    check(len(run.read(shared, run.b)["messages"]) == 6, "missing Herdr blocked mail")
    run.stop()
    run.set_fake(mode="uncertain")
    run.start(notices=True)
    run.wait(
        lambda: run.status(run.b)["targets"][0]["counts"].get("uncertain", 0) == 1,
        "ambiguous notice not uncertain",
    )
    calls_before = len(run.prompts())
    run.stop()
    run.set_fake(mode="crash")
    run.start(notices=True)
    check(len(run.prompts()) == calls_before, "uncertain notice retried on restart")
    crash_op, crash_body = run.op(), "Crash after simulated input @03-worker"
    run.mail(shared, crash_body, operation=crash_op, ambiguous=True)
    check(
        run.process.wait(timeout=10) == -signal.SIGKILL,
        "service was not actually killed",
    )
    run.stop()
    run.set_fake(mode="")
    run.start(notices=True)
    recovered = run.mail(shared, crash_body, operation=crash_op)
    check(recovered["revision"] == 7, "crash retry lost original sequence")
    check(
        run.status(run.b)["targets"][0]["counts"].get("uncertain", 0) == 2,
        "restart did not quarantine captured refs",
    )
    calls_before = len(run.prompts())
    run.mail(shared, "New after uncertainty @03-worker")
    run.wait(
        lambda: (
            len(run.prompts()) == calls_before + 1
            and run.status(run.b)["targets"][0]["counts"].get("submitted", 0) == 2
        ),
        "fresh notice blocked by uncertainty",
    )
    old_b = dict(run.b)
    run.binding(run.b, "replacement", rebind=True)
    run.cli(
        "task",
        "show",
        *run.context,
        "--task",
        shared,
        actor=old_b,
        expect="unauthorized",
    )
    run.orch = run.b
    run.set_fake(status="unknown")
    run.mail(shared, "Paused closed work @03-worker")
    run.wait(
        lambda: run.status(run.b)["targets"][0]["counts"].get("pending", 0) == 1,
        "pending notice missing",
    )
    run.cli(
        "session",
        "close",
        *run.context,
        "--expected-revision",
        run.session()["revision"],
        "--operation-id",
        run.op(),
    )
    run.cli(
        "mail",
        "send",
        *run.context,
        "--task",
        shared,
        "--body",
        "Closed",
        "--operation-id",
        run.op(),
        actor=run.a,
        expect="session_closed",
    )
    check(
        run.mail(shared, first_body, operation=first_op) == first,
        "closure broke authorized receipt recovery",
    )
    check(len(run.read(shared, run.b)["messages"]) == 9, "closed Session lost history")
    run.cli(
        "session",
        "reopen",
        *run.context,
        "--expected-revision",
        run.session()["revision"],
        "--operation-id",
        run.op(),
    )
    check(run.show(shared)["progress"] == "waiting", "closure changed Task progress")
    browser, _ = run.browser()
    # The browser credential reads the Task mail of its Session, read-only.
    check(
        len(run.data("/v1/mail/read", {"task_id": shared}, browser)["messages"]) == 9,
        "browser lost Task mail read",
    )
    run.api(
        "/v1/mail/send",
        {"task_id": shared, "body": "Denied", "operation_id": run.op()},
        browser,
        expect="forbidden",
    )
    run.api("/v1/mail/check", {}, browser, expect="forbidden")
    run.api(
        "/v1/task/create",
        {"title": "Denied", "operation_id": run.op()},
        browser,
        expect="forbidden",
    )
    other_session = run.cli(
        "session",
        "create",
        "--workspace",
        run.ref["workspace_id"],
        "--name",
        "Other scope",
        "--operation-id",
        run.op(),
    )["resource_id"]
    run.api(
        "/v1/session/snapshot",
        {"session_id": other_session},
        browser,
        expect="forbidden",
    )
    retained, history = run.read(shared), run.pages("history", shared)
    snapshot = run.backup("workflow")
    stored = inspect_backup(snapshot)
    check(stored["rows"]["mail_messages"] == 9, "body duplicated/lost")
    check(
        stored["notice_states"].get("suppressed") == 1
        and stored["notice_states"].get("uncertain") == 2,
        "terminal notice states lost",
    )
    database_id = run.discovery["database_id"]
    operator = (run.state / "operator.json").read_bytes()
    run.stop()
    run.state = run.root / "restored"
    run.state.mkdir(mode=0o700)
    shutil.copyfile(snapshot, run.state / "state.db")
    (run.state / "state.db").chmod(0o600)
    (run.state / "operator.json").write_bytes(operator)
    (run.state / "operator.json").chmod(0o600)
    run.start()
    check(
        run.discovery["database_id"] == database_id
        and run.read(shared) == retained
        and run.pages("history", shared) == history,
        "restore lost IDs/history/cursor positions",
    )
    check(
        run.mail(shared, first_body, operation=first_op) == first,
        "restored receipt changed",
    )
    check(
        len(run.read(shared, cursor="")["messages"]) == 9, "lost cursor did not replay"
    )
    check(
        not run.read(shared, cursor=retained["cursor"])["messages"],
        "saved cursor ignored after restore",
    )
    run.cli(
        "task",
        "show",
        *run.context,
        "--task",
        shared,
        actor=old_b,
        expect="unauthorized",
    )
    return {
        "result": "PASS",
        "actual_cli_calls": run.calls,
        "actual_http_calls": run.api_calls,
        "restored_messages": 9,
        "restored_task_history_entries": len(history),
        "body_copies": stored["rows"]["mail_messages"],
        "notice_states": stored["notice_states"],
        "real_service_SIGKILL": True,
        "response_drop_after_commit": True,
        "completed_backup_restore": True,
    }


def engine_faults():
    names = "TestMutationAtomicFailureAndDiskFull|TestReadOnlyStorageFailure|TestMailProcessCrashRollbackAndOriginalReceiptRecovery|TestNotificationsCrashRecoveryNeverResendsCapturedRefs|TestSafeBackupRestoreAndMissingCredential"
    result = subprocess.run(
        [
            "go",
            "test",
            "-json",
            "./internal/stack",
            "-run",
            "^(" + names + ")$",
            "-count=1",
        ],
        cwd=REPO,
        env={k: v for k, v in os.environ.items() if not k.startswith("ASTACK_")},
        capture_output=True,
        timeout=90,
    )
    check(result.returncode == 0, "existing real engine/recovery fixtures failed")
    expected, passed = set(names.split("|")), set()
    for line in result.stdout.splitlines():
        event = json.loads(line)
        if event.get("Test") in expected:
            check(
                event.get("Action") not in ("skip", "fail"),
                "selected engine fixture skipped/failed",
            )
            if event.get("Action") == "pass":
                passed.add(event["Test"])
    check(passed == expected, "selected engine fixtures missing actual PASS events")
    return {
        "result": "PASS",
        "tests": names.split("|"),
        "scope": "actual SQLite_FULL/read-only engine errors and subprocess crash/restore; no physical disk/power fault simulation",
    }


def percentiles(values):
    ordered = sorted(values)
    check(bool(ordered), "empty timing sample")
    return {
        "samples": len(ordered),
        "p50_us": round(ordered[math.ceil(0.50 * len(ordered)) - 1] / 1000, 3),
        "p95_us": round(ordered[math.ceil(0.95 * len(ordered)) - 1] / 1000, 3),
    }


def timed(action):
    samples = []
    for index in range(WARMUP + SAMPLES):
        start = time.monotonic_ns()
        action()
        if index >= WARMUP:
            samples.append(time.monotonic_ns() - start)
    return percentiles(samples)


def populate(run, count):
    run.bootstrap(long_name=True)
    third = run.register("04-worker", run.orch)
    tasks = []
    for index in range(count):
        task = run.data(
            "/v1/task/create",
            {"operation_id": run.op(), "title": f"Task {index:04d}"},
            run.orch,
        )
        task_id, revision = task["resource_id"], task["revision"]
        tasks.append(task_id)
        assigned = (
            [run.a, run.b, run.orch, third]
            if index == 0
            else ([] if index % 7 == 0 else [run.a if index % 2 else run.b])
        )
        for actor in assigned:
            revision = run.data(
                "/v1/task/assign",
                {
                    "task_id": task_id,
                    "agent_id": actor["id"],
                    "expected_revision": revision,
                    "operation_id": run.op(),
                },
                run.orch,
            )["revision"]
        if assigned:
            revision = run.data(
                "/v1/task/report",
                {
                    "task_id": task_id,
                    "report": "Literal <img src=x onerror=alert(1)> report "
                    + str(index),
                    "expected_revision": revision,
                    "operation_id": run.op(),
                },
                assigned[0],
            )["revision"]
            progress = ["waiting", "working", "finished", "cancelled"][index % 4]
            if progress != "waiting":
                payload = {
                    "task_id": task_id,
                    "progress": progress,
                    "expected_revision": revision,
                    "operation_id": run.op(),
                }
                payload["summary" if progress == "finished" else "reason"] = (
                    "Synthetic fixture state"
                )
                revision = run.data("/v1/task/status", payload, assigned[0])["revision"]
            if index in (0, 1):
                run.data(
                    "/v1/task/attention/set",
                    {
                        "task_id": task_id,
                        "question": f"Explicit Operator question {index}",
                        "expected_revision": revision,
                        "operation_id": run.op(),
                    },
                    assigned[0],
                )
        if index:
            run.data(
                "/v1/dependency/add",
                {
                    "prerequisite_id": tasks[(index - 1) // 2],
                    "dependent_id": task_id,
                    "expected_graph_revision": run.session()["graph_revision"],
                    "operation_id": run.op(),
                },
                run.orch,
            )
    run.change(
        "reference add",
        tasks[0],
        run.a,
        "--location",
        "/display-only/fixture.txt",
        "--reference-revision",
        "fixture-revision",
    )
    return tasks, third


def complete_poll(run, count):
    manifest = run.data("/v1/session/snapshot", {"known_revision": 0}, run.a)
    revision = manifest["session"]["read_revision"]
    total = 0
    for kind in ("tasks", "agents", "assignments", "dependencies"):
        after, seen, cursors = "", 0, set()
        while True:
            page = run.data(
                "/v1/session/page",
                {"kind": kind, "read_revision": revision, "limit": 100, "after": after},
                run.a,
            )
            check(page["read_revision"] == revision, "poll mixed read revisions")
            seen += len(page["items"])
            next_cursor = page.get("next", "")
            if not next_cursor:
                break
            check(
                page["items"] and next_cursor != after and next_cursor not in cursors,
                "poll continuation made no progress",
            )
            cursors.add(next_cursor)
            after = next_cursor
        check(seen == manifest["counts"][kind], "poll silently lost collection rows")
        if kind == "tasks":
            total = seen
    check(total == count, "poll omitted Tasks")


def machine(binary, temporary):
    mounts = []
    for path in (temporary, Path("/tmp")):
        probe = subprocess.run(
            ["findmnt", "-T", str(path), "-J", "-o", "FSTYPE,OPTIONS"],
            capture_output=True,
            text=True,
        )
        check(probe.returncode == 0, "filesystem classification unavailable")
        filesystem = json.loads(probe.stdout)["filesystems"][0]
        mounts.append(
            {
                "location": "fixture root" if path == temporary else "/tmp",
                "type": filesystem["fstype"],
                "tmpfs": filesystem["fstype"] == "tmpfs",
                "synchronous_storage_claim": False,
            }
        )
    cpu = next(
        (
            line.split(":", 1)[1].strip()
            for line in Path("/proc/cpuinfo").read_text().splitlines()
            if line.startswith("model name")
        ),
        platform.machine(),
    )
    return {
        "cpu": cpu,
        "os": platform.system(),
        "architecture": platform.machine(),
        "kernel": platform.release(),
        "go": subprocess.check_output(["go", "version"], text=True).strip(),
        "python": platform.python_version(),
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "sqlite_analysis": sqlite3.sqlite_version,
        "filesystems": mounts,
        "adapter": "strict stdlib Python fake; no live Herdr",
        "simulated_herdr": "0.9.5",
        "declared_fake_harness": "codex/0.164.0",
    }


def measurements(binary):
    result = {
        "warmup_per_timing": WARMUP,
        "samples_per_timing": SAMPLES,
        "percentile_method": "nearest rank",
        "clock": "external monotonic; batch stamps separately use wall clock",
        "task_sizes": [],
        "setup": "actual CLI enrollment, authenticated HTTP bulk fixture writes; setup is not CLI latency",
    }
    for count in (10, 100, 1000):
        with tempfile.TemporaryDirectory(prefix="a-stack-v1-measure-") as temporary:
            run = Run(binary, Path(temporary))
            try:
                run.stage = f"measure {count} Tasks"
                tasks, third = populate(run, count)
                if count == 10:
                    result["machine"] = machine(binary, run.root)
                    result["sqlite_service"] = run.cli("health")["sqlite_version"]
                first_calls = run.api_calls
                complete_poll(run, count)
                poll_requests = run.api_calls - first_calls
                # B023: timed() and run.wait() call these closures at once, inside this iteration.
                poll_timing = timed(lambda: complete_poll(run, count))  # noqa: B023
                cli_timing = timed(
                    lambda: run.cli(
                        "task", "list", *run.context, "--limit", 100, actor=run.a
                    )
                )  # noqa: B023
                _, manifest, headers, _ = run.api(
                    "/v1/session/snapshot", {"known_revision": 0}, run.a
                )
                revision = manifest["data"]["session"]["read_revision"]
                tag = next(
                    value for key, value in headers.items() if key.lower() == "etag"
                )
                idle_bytes = []

                def idle():
                    status, _, _, size = run.api(
                        "/v1/session/snapshot",
                        {"known_revision": revision},
                        run.a,
                        headers={"If-None-Match": tag},
                    )  # noqa: B023
                    check(
                        status == 304 and size == 0,
                        "idle poll not an authenticated empty 304",
                    )
                    idle_bytes.append(size)  # noqa: B023

                idle_timing = timed(idle)
                before = run.cli("storage", "status")
                baseline = inspect_backup(run.backup("tasks"))
                row = {
                    "tasks": count,
                    "dependencies": count - 1,
                    "fixture_rows": baseline["rows"],
                    "storage": before,
                    "task_snapshot": baseline,
                    "changed_poll_all_collections": poll_timing,
                    "changed_poll_requests": poll_requests,
                    "cli_task_list_first_page": cli_timing,
                    "idle_conditional_poll": idle_timing,
                    "idle_requests_including_warmup": len(idle_bytes),
                    "idle_response_body_bytes": sum(idle_bytes),
                }
                result["task_sizes"].append(row)
                if count == 1000:
                    for actor, suffix in [(run.b, "b"), (run.orch, "o"), (third, "c")]:
                        run.binding(actor, suffix)
                    body_bytes = 0
                    start = time.monotonic_ns()
                    for index in range(10000):
                        body = f"Measurement {index:05d} @03-worker @01-orchestrator @04-worker"
                        body_bytes += len(body.encode())
                        run.data(
                            "/v1/mail/send",
                            {
                                "task_id": tasks[0],
                                "body": body,
                                "operation_id": run.op(),
                            },
                            run.a,
                        )
                    setup_ms = (time.monotonic_ns() - start) / 1e6
                    retained = inspect_backup(run.backup("ten-thousand-pending"))
                    check(
                        retained["rows"]["mail_messages"] == 10000
                        and retained["body_bytes"] == body_bytes == 560000
                        and retained["rows"]["mail_mentions"] == 30000,
                        "10k body/mention copies lost or duplicated",
                    )
                    run.stop()
                    run.start(notices=True)

                    def submitted():
                        return sum(
                            t["counts"].get("submitted", 0)
                            for t in run.status()["targets"]
                        )  # noqa: B023

                    run.wait(
                        lambda: submitted() == 30000,
                        "10k burst did not drain",
                        timeout=90,
                    )
                    check(
                        len(run.prompts()) == 60,
                        "already-waiting burst did not use 500-ref batches",
                    )
                    burst_storage = run.cli("storage", "status")
                    burst_backup = inspect_backup(run.backup("ten-thousand-submitted"))
                    check(
                        burst_backup["rows"]["notice_batch_refs"] == 30000
                        and burst_backup["rows"]["notice_batches"] == 60,
                        "burst coverage rows wrong",
                    )
                    send_samples, observed_samples, sample_ids = [], [], []
                    for index in range(WARMUP + SAMPLES):
                        expected = submitted() + 1
                        start = time.monotonic_ns()
                        receipt = run.mail(tasks[0], f"Timed {index:02d} @03-worker")
                        received = time.monotonic_ns()
                        run.wait(
                            lambda: submitted() == expected,
                            "isolated notice not durably submitted",
                        )  # noqa: B023
                        end = time.monotonic_ns()
                        if index >= WARMUP:
                            send_samples.append(received - start)
                            observed_samples.append(end - start)
                            sample_ids.append(receipt["resource_id"])
                    final_path = run.backup("single-send-samples")
                    claim_spans = []
                    with sqlite3.connect(
                        final_path.as_uri() + "?mode=ro", uri=True
                    ) as database:
                        for message in sample_ids:
                            created, updated, state = database.execute(
                                "SELECT b.created_at,b.updated_at,b.state FROM notice_batches b JOIN notice_batch_refs r ON r.batch_id=b.id WHERE r.message_id=?",
                                (message,),
                            ).fetchone()
                            check(
                                state == "submitted",
                                "sample submission did not persist",
                            )
                            claim_spans.append(
                                int(
                                    (
                                        datetime.datetime.fromisoformat(updated)
                                        - datetime.datetime.fromisoformat(created)
                                    ).total_seconds()
                                    * 1e9
                                )
                            )
                    result["mail_10000"] = {
                        "messages": 10000,
                        "body_utf8_bytes_each": 56,
                        "stored_body_bytes": body_bytes,
                        "nonself_recipients_each": 3,
                        "pending_references": 30000,
                        "fake_submissions": 60,
                        "http_setup_ms_not_cli_latency": round(setup_ms, 3),
                        "pending_backup": retained,
                        "submitted_backup": burst_backup,
                        "physical_after_burst": burst_storage,
                        "cli_send": percentiles(send_samples),
                        "send_start_to_observed_submitted": percentiles(
                            observed_samples
                        ),
                        "claim_stamp_to_outcome_stamp": percentiles(claim_spans),
                        "observation_poll_interval_ms": 20,
                        "batch_stamp_limit": "claim/outcome stamps are inside transactions, not pure postcommit latency",
                        "isolated_timing_messages_including_warmup": WARMUP + SAMPLES,
                    }
            finally:
                run.stop()
    return result


def browser_fixture(binary, directory):
    root = directory.resolve()
    check(
        root.is_absolute()
        and all(not (parent / ".git").exists() for parent in (root, *root.parents)),
        "fixture must be outside project checkouts",
    )
    if root.exists():
        check(
            root.is_dir()
            and root.stat().st_uid == os.getuid()
            and root.stat().st_mode & 0o077 == 0
            and not any(root.iterdir()),
            "fixture directory must be private and empty",
        )
    else:
        root.mkdir(mode=0o700)
    run, context = None, root / "browser-context.json"
    stopping = threading.Event()
    previous = signal.signal(signal.SIGTERM, lambda *_: stopping.set())
    try:
        run = Run(binary, root)
        tasks, _ = populate(run, 10)
        check(not stopping.is_set(), "fixture stopped during startup")
        browser, view = run.browser()
        private_json(
            context,
            {
                "view_url": view,
                "credential_file": str(browser["file"]),
                **run.ref,
                "task_ids": tasks,
            },
        )
        print(
            json.dumps(
                {
                    "browser_fixture_context": str(context),
                    "lifetime_seconds": 600,
                    "stop_marker": str(root / "stop"),
                }
            ),
            flush=True,
        )
        # Bounded disposable test service; it never controls a native agent.
        deadline = time.monotonic() + 600
        while (
            time.monotonic() < deadline
            and not (root / "stop").exists()
            and not stopping.wait(0.25)
        ):
            pass
    except KeyboardInterrupt:
        pass
    finally:
        try:
            if run:
                run.stop()
        finally:
            signal.signal(signal.SIGTERM, previous)
            if run:
                shutil.rmtree(run.credentials, ignore_errors=True)
                (run.state / "operator.json").unlink(missing_ok=True)
            context.unlink(missing_ok=True)
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--binary",
        type=Path,
        help="use this existing product build instead of building current source",
    )
    parser.add_argument(
        "--quick", action="store_true", help="workflow and engine faults only"
    )
    parser.add_argument("--output", type=Path, help="write sanitized measurement JSON")
    parser.add_argument(
        "--browser-fixture",
        type=Path,
        help="hold a private disposable read-only browser fixture for ten minutes",
    )
    args = parser.parse_args()
    binary = (args.binary or REPO / ".tmp/a-stack-v1").resolve()
    check(args.binary is None or binary.is_file(), "explicit binary does not exist")
    if args.binary is None:
        binary.parent.mkdir(parents=True, exist_ok=True)
        built = subprocess.run(
            ["go", "build", "-o", str(binary), "./cmd/a-stack"],
            cwd=REPO,
            capture_output=True,
            timeout=180,
        )
        check(built.returncode == 0, "product build failed")
    run = None
    try:
        if args.browser_fixture:
            return browser_fixture(binary, args.browser_fixture)
        with tempfile.TemporaryDirectory(prefix="a-stack-v1-") as temporary:
            root = Path(temporary)
            run = Run(binary, root)
            try:
                result = {
                    "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                    "binary_selection": "explicit artifact"
                    if args.binary
                    else "current source build",
                    "workflow": scenario(run),
                }
            finally:
                run.stop()
            result["engine_faults"] = engine_faults()
            run.stage = "measurements"
            result["measurements"] = (
                measurements(binary)
                if not args.quick
                else {"status": "skipped by --quick"}
            )
            if args.output:
                args.output.write_text(json.dumps(result, indent=2) + "\n")
            print(json.dumps(result, sort_keys=True))
    except (Exception, KeyboardInterrupt) as error:
        message = (
            str(error) if isinstance(error, AssertionError) else type(error).__name__
        )
        print(
            json.dumps({"error": message, "stage": run.stage if run else "build"}),
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--fake-herdr":
        sys.exit(fake_herdr(sys.argv[2], sys.argv[3:]))
    sys.exit(main())
