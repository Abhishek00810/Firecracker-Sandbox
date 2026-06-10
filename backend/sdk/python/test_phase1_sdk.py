import asyncio
import unittest
from unittest.mock import patch

from renderops import AsyncSession, RunResult, Sandbox, Session


class Phase1SDKTest(unittest.TestCase):
    def test_session_sends_env_and_tier(self):
        calls = []

        class Response:
            status_code = 201
            text = ""

            def json(self):
                return {"session": {"session_id": "sess-1", "tier": "pro"}}

        def fake_post(url, json=None, headers=None, timeout=None):
            calls.append({"url": url, "json": json, "headers": headers, "timeout": timeout})
            return Response()

        with patch("requests.post", fake_post):
            sb = Sandbox(api_key="ro_test", base_url="http://api.test", timeout=12)
            sess = sb.session(env={"GITHUB_TOKEN": "ghp_x"}, tier="pro")

        self.assertIsInstance(sess, Session)
        self.assertEqual(sess.id, "sess-1")
        self.assertEqual(sess.tier, "pro")
        self.assertEqual(
            calls,
            [
                {
                    "url": "http://api.test/session",
                    "json": {"env": {"GITHUB_TOKEN": "ghp_x"}, "tier": "pro"},
                    "headers": {
                        "Content-Type": "application/json",
                        "Authorization": "Bearer ro_test",
                    },
                    "timeout": 12,
                }
            ],
        )

    def test_session_without_options_sends_empty_body(self):
        bodies = []

        class Response:
            status_code = 201
            text = ""

            def json(self):
                return {"session": {"session_id": "sess-1", "tier": "free"}}

        def fake_post(url, json=None, headers=None, timeout=None):
            bodies.append(json)
            return Response()

        with patch("requests.post", fake_post):
            sb = Sandbox(api_key="ro_test")
            sb.session()

        self.assertEqual(bodies, [{}])

    def test_session_exec_posts_shell_command_and_timeout(self):
        class FakeClient:
            def __init__(self):
                self.calls = []

            def _post(self, path, body):
                self.calls.append((path, body))
                return {
                    "request_id": "req-1",
                    "result": {
                        "stdout": "ok\n",
                        "stderr": "",
                        "exit_code": 0,
                        "duration_ms": 12.5,
                        "termination_reason": "success",
                    },
                }

        client = FakeClient()
        sess = Session("sess-1", "pro", client)

        result = sess.exec("git status", timeout=45)

        self.assertIsInstance(result, RunResult)
        self.assertEqual(result.stdout, "ok\n")
        self.assertTrue(result.ok)
        self.assertEqual(client.calls, [("/session/sess-1/exec", {"command": "git status", "timeout": 45})])

    def test_async_session_exec_posts_shell_command_and_timeout(self):
        class FakeClient:
            def __init__(self):
                self.calls = []

            async def _post(self, path, body):
                self.calls.append((path, body))
                return {
                    "request_id": "req-1",
                    "result": {
                        "stdout": "ok\n",
                        "stderr": "",
                        "exit_code": 0,
                        "duration_ms": 12.5,
                        "termination_reason": "success",
                    },
                }

        client = FakeClient()
        sess = AsyncSession("sess-1", "pro", client)

        result = asyncio.run(sess.exec("pytest -q", timeout=60))

        self.assertIsInstance(result, RunResult)
        self.assertEqual(result.stdout, "ok\n")
        self.assertTrue(result.ok)
        self.assertEqual(client.calls, [("/session/sess-1/exec", {"command": "pytest -q", "timeout": 60})])


if __name__ == "__main__":
    unittest.main()
