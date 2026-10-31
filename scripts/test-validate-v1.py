#!/usr/bin/env python3
"""Regression tests for the disposable validation runner; build .tmp/a-stack first."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

SPEC = importlib.util.spec_from_file_location(
    "validation", Path(__file__).with_name("validate-v1.py")
)
v = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(v)


class PagesTest(unittest.TestCase):
    def test_discards_partial_collection(self):
        run = object.__new__(v.Run)
        run.session = Mock(side_effect=[{"read_revision": 1}, {"read_revision": 2}])
        run.data = Mock(
            side_effect=[
                {"read_revision": 1, "items": ["old"], "next": "cursor"},
                v.SnapshotConflict("changed"),
                {"read_revision": 2, "items": ["new"], "next": "cursor"},
                {"read_revision": 2, "items": ["tail"]},
            ]
        )
        self.assertEqual(run.pages("history", "task", "actor"), ["new", "tail"])
        self.assertEqual(
            [call.args[1]["after"] for call in run.data.call_args_list],
            ["", "cursor", "", "cursor"],
        )
        self.assertEqual(run.data.call_args.args[2], "actor")

    def test_bounded_restarts_and_other_failures(self):
        for error, attempts in [(v.SnapshotConflict("changed"), 3), (ValueError(), 1)]:
            with self.subTest(error=type(error).__name__):
                run = object.__new__(v.Run)
                run.session = Mock(return_value={"read_revision": 1})
                run.data = Mock(side_effect=error)
                with self.assertRaises(type(error)):
                    run.pages("history")
                self.assertEqual(run.session.call_count, attempts)

    def test_only_page_revision_conflicts_are_restartable(self):
        for path, status, error, restartable in [
            (
                "/v1/session/page",
                409,
                {"code": "conflict", "current_revision": 2},
                True,
            ),
            ("/v1/session/page", 409, {"code": "conflict"}, False),
            ("/v1/session/page", 403, {"code": "forbidden"}, False),
            ("/v1/session/page", 500, {"code": "storage_unavailable"}, False),
            ("/v1/task/show", 409, {"code": "conflict", "current_revision": 2}, False),
        ]:
            with self.subTest(path=path, status=status, error=error):
                run = object.__new__(v.Run)
                run.state, run.ref, run.api_calls = Path("unused"), {}, 0
                run.discovery = {"instance_id": "test"}
                run.connection = Mock()
                response = run.connection.getresponse.return_value
                response.status = status
                response.read.return_value = json.dumps({"error": error}).encode()
                with patch.object(Path, "read_text", return_value='{"token":"test"}'):
                    with self.assertRaises(AssertionError) as raised:
                        run.api(path)
                self.assertEqual(
                    isinstance(raised.exception, v.SnapshotConflict), restartable
                )

    def test_own_read_between_snapshot_and_page(self):
        with tempfile.TemporaryDirectory(prefix="a-stack-page-race-") as directory:
            run = v.Run(v.REPO / ".tmp/a-stack", Path(directory))
            try:
                run.bootstrap()
                task = run.task("Stable history")
                run.assign(task, run.a)
                expected = run.pages("history", task)
                original_session = run.session
                injected = False

                def session():
                    nonlocal injected
                    result = original_session()
                    if not injected:
                        injected = True
                        run.read(task)
                    return result

                with patch.object(run, "session", side_effect=session):
                    self.assertEqual(run.pages("history", task), expected)
                self.assertTrue(injected)
            finally:
                run.stop()


if __name__ == "__main__":
    unittest.main()
