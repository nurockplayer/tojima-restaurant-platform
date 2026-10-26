"""Keyless tests with local fake CLI subprocesses; no network/provider calls."""
import importlib.util
from contextlib import contextmanager, nullcontext, redirect_stdout, redirect_stderr
import io
import json
from pathlib import Path
from types import SimpleNamespace
import re
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock
import warnings


SPEC = importlib.util.spec_from_file_location("stripe_sandbox_runner", Path(__file__).with_name("run-stripe-sandbox.py"))
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class EnvironmentTests(unittest.TestCase):
    def fixture(self):
        return {
            "TOJIMA_STRIPE_MODE": runner.MODE,
            "TOJIMA_STRIPE_API_VERSION": runner.API_VERSION,
            "TOJIMA_STRIPE_TEST_SECRET_KEY": "sk_test_" + "x" * 24,
            "TOJIMA_STRIPE_TEST_ACCOUNT_A": "acct_syntheticA",
            "TOJIMA_STRIPE_TEST_ACCOUNT_B": "acct_syntheticB",
            "TOJIMA_STRIPE_TEST_ACCOUNT_C": "acct_syntheticC",
            "TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY": "10000",
            "TEST_DATABASE_URL": "postgres://postgres@127.0.0.1/tojima_test?sslmode=disable",
        }

    def test_valid_synthetic_environment(self):
        env = self.fixture()
        self.assertEqual(runner.checked_environment(env), env)

    def test_fail_closed_and_redacted(self):
        for key, bad in (
            ("TOJIMA_STRIPE_MODE", "UNCONFIGURED"),
            ("TOJIMA_STRIPE_API_VERSION", "latest"),
            ("TOJIMA_STRIPE_TEST_SECRET_KEY", "sk_live_" + "x" * 24),
            ("TOJIMA_STRIPE_TEST_SECRET_KEY", "rk_test_" + "x" * 24),
            ("TOJIMA_STRIPE_TEST_ACCOUNT_A", runner.PLATFORM_ACCOUNT),
            ("TOJIMA_STRIPE_TEST_ACCOUNT_B", "acct_syntheticA"),
            ("TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "0"),
            ("TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "10001"),
            ("TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "9223372036854775808"),
            ("TEST_DATABASE_URL", ""),
        ):
            with self.subTest(key=key):
                env = self.fixture(); env[key] = bad
                with self.assertRaises(ValueError) as caught:
                    runner.checked_environment(env)
                self.assertNotIn(env["TOJIMA_STRIPE_TEST_SECRET_KEY"], str(caught.exception))


    def test_hidden_prompt_fails_before_echo_fallback(self):
        fallback_read = []
        def unavailable_echo(*_args):
            warnings.warn("terminal echo unavailable", runner.getpass.GetPassWarning)
            fallback_read.append(True)
            return "unexpected fallback"
        with mock.patch.object(runner.getpass, "getpass", unavailable_echo):
            with self.assertRaises(ValueError):
                runner.hidden_key_input()
        self.assertEqual(fallback_read, [])

    @contextmanager
    def mocked_run(self, real_listener=None):
        source = dict(self.fixture(), PATH="/usr/bin", HOME="/test/home",
                      STRIPE_API_KEY="sk_test_inherited",
                      STRIPE_WEBHOOK_SECRET="whsec_inherited",
                      TOJIMA_STRIPE_TEST_WEBHOOK_SECRET="whsec_stale",
                      UNRELATED_SECRET="unrelated")
        listener = real_listener if real_listener is not None else mock.Mock(pid=101)
        runtime = mock.Mock(pid=102)
        runtime.poll.return_value = None
        if real_listener is None:
            listener.poll.return_value = None
        args = SimpleNamespace(prompt_key=False, keychain_service=runner.DEFAULT_KEYCHAIN_SERVICE,
                               port=8083, tenant="A",
                               runtime_binary="/test/runtime", credentials="/test/credentials",
                               startup_check=True)
        real_killpg = runner.os.killpg

        def kill_real_listener(pid, signum):
            if real_listener is not None and pid == real_listener.pid:
                real_killpg(pid, signum)

        with (
            mock.patch.dict(runner.os.environ, source, clear=True),
            mock.patch.object(runner.shutil, "which", return_value="/test/stripe"),
            mock.patch.object(runner.Path, "is_file", return_value=True),
            mock.patch.object(runner.socket, "socket"),
            mock.patch.object(runner.subprocess, "run", side_effect=[
                SimpleNamespace(returncode=0, stdout=b"stripe version 1.54.0\n"),
                SimpleNamespace(returncode=0, stdout=b"whsec_synthetic123\n"),
            ]) as cli,
            mock.patch.object(runner, "keychain_key", return_value=self.fixture()["TOJIMA_STRIPE_TEST_SECRET_KEY"]),
            mock.patch.object(runner.urllib.request, "urlopen",
                              return_value=io.BytesIO(json.dumps({"id": runner.PLATFORM_ACCOUNT}).encode())),
            mock.patch.object(runner.subprocess, "Popen", side_effect=[listener, runtime]) as popen,
            mock.patch.object(runner, "ListenerOutput") if real_listener is None else nullcontext(),
            mock.patch.object(runner, "startup_check") as check,
            mock.patch.object(runner.os, "killpg", side_effect=kill_real_listener) as killpg,
            mock.patch("builtins.print") if real_listener is None else nullcontext(),
        ):
            if real_listener is None:
                runner.ListenerOutput.return_value.ended.is_set.return_value = False
            yield SimpleNamespace(args=args, source=source, listener=listener, runtime=runtime,
                                  cli=cli, popen=popen, check=check, killpg=killpg)

    def test_child_credentials_are_scoped(self):
        with self.mocked_run() as launch:
            self.assertEqual(runner.run(launch.args), 0)
            base = {"PATH": launch.source["PATH"], "HOME": launch.source["HOME"]}
            self.assertEqual(launch.cli.call_args_list[0].kwargs["env"], base)
            cli_env = dict(base, STRIPE_API_KEY=launch.source["TOJIMA_STRIPE_TEST_SECRET_KEY"],
                           STRIPE_DEVICE_NAME="tojima-isolated-sandbox")
            self.assertEqual(launch.cli.call_args_list[1].kwargs["env"], cli_env)
            self.assertEqual(launch.popen.call_args_list[0].kwargs["env"], cli_env)
            runtime_env = dict(base, **self.fixture(),
                               TOJIMA_STRIPE_TEST_WEBHOOK_SECRET="whsec_synthetic123")
            self.assertEqual(launch.popen.call_args_list[1].kwargs["env"], runtime_env)

    def test_default_merchant_accounts_are_used_and_overrides_win(self):
        env = self.fixture()
        for tenant, account in runner.DEFAULT_MERCHANT_ACCOUNTS.items():
            env.pop("TOJIMA_STRIPE_TEST_ACCOUNT_" + tenant)
        checked = runner.checked_environment(env)
        for tenant, account in runner.DEFAULT_MERCHANT_ACCOUNTS.items():
            self.assertEqual(checked["TOJIMA_STRIPE_TEST_ACCOUNT_" + tenant], account)
        env["TOJIMA_STRIPE_TEST_ACCOUNT_A"] = "acct_customA"
        self.assertEqual(runner.checked_environment(env)["TOJIMA_STRIPE_TEST_ACCOUNT_A"], "acct_customA")

    def test_platform_account_guard_fails_before_cli_on_wrong_account(self):
        with self.mocked_run() as launch, mock.patch.object(
                runner.urllib.request, "urlopen",
                return_value=io.BytesIO(b'{"id":"acct_wrong"}')):
            with self.assertRaisesRegex(ValueError, "approved Stripe Sandbox platform account"):
                runner.run(launch.args)
            launch.cli.assert_not_called()
            launch.popen.assert_not_called()

    def test_keychain_failure_is_clear_and_precedes_cli(self):
        with self.mocked_run() as launch, mock.patch.object(
                runner, "keychain_key", side_effect=ValueError("Keychain item 'tojima-stripe-sandbox' is missing or inaccessible")):
            with self.assertRaisesRegex(ValueError, "Keychain item .* missing or inaccessible"):
                runner.run(launch.args)
            launch.cli.assert_not_called()

    def test_shared_keychain_reader_uses_service_and_rejects_non_test_keys(self):
        with mock.patch.object(runner.subprocess, "run", return_value=SimpleNamespace(
                returncode=0, stdout="sk_test_" + "x" * 24 + "\n")) as security:
            self.assertEqual(runner.keychain_key("custom-sandbox-key"), "sk_test_" + "x" * 24)
            self.assertEqual(security.call_args.args[0], [
                "security", "find-generic-password", "-s", "custom-sandbox-key", "-w"])
        for result, message in (
                (SimpleNamespace(returncode=44, stdout=""), "missing or inaccessible"),
                (SimpleNamespace(returncode=0, stdout="sk_live_" + "x" * 24), "not contain a valid sk_test_ key")):
            with self.subTest(message=message), mock.patch.object(runner.subprocess, "run", return_value=result):
                with self.assertRaisesRegex(ValueError, message):
                    runner.keychain_key("custom-sandbox-key")

    def test_forwarded_events_match_service_and_provider_projections(self):
        root = Path(__file__).resolve().parents[1]
        event_pattern = r'"((?:checkout\.session|payment_intent|refund|charge)\.[a-z_]+)"'
        handled = set(re.findall(event_pattern, (root / "internal/payments/work.go").read_text()))
        projected = set(re.findall(event_pattern, (root / "internal/stripeprovider/webhook.go").read_text()))
        events = runner.EVENTS.split(",")
        self.assertEqual(set(events), handled | projected)
        self.assertEqual(len(events), len(set(events)))
        self.assertTrue({"refund.updated", "refund.failed", "charge.refunded"} <= set(events))
        with self.mocked_run() as launch:
            self.assertEqual(runner.run(launch.args), 0)
            argv = launch.popen.call_args_list[0].args[0]
            self.assertEqual(argv[argv.index("--events") + 1], runner.EVENTS)
            self.assertEqual(argv[argv.index("--forward-connect-to") + 1],
                             "http://127.0.0.1:8083/v1/payments/stripe/webhook")

    def test_bad_cli_version_never_receives_credentials_or_launches_children(self):
        with self.mocked_run() as launch:
            launch.cli.side_effect = [SimpleNamespace(returncode=0, stdout=b"stripe version 0.0.0\n")]
            with self.assertRaises(ValueError):
                runner.run(launch.args)
            launch.popen.assert_not_called()
            self.assertEqual(launch.cli.call_count, 1)
            self.assertNotIn("STRIPE_API_KEY", launch.cli.call_args.kwargs["env"])
            self.assertNotIn("TOJIMA_STRIPE_TEST_SECRET_KEY", launch.cli.call_args.kwargs["env"])

    def test_repeated_interrupts_are_deferred_until_both_groups_stop(self):
        for signum in (runner.signal.SIGINT, runner.signal.SIGTERM):
            with self.subTest(signal=signum), self.mocked_run() as launch:
                delivered = []

                def interrupt(received, _frame):
                    delivered.append(received)
                    raise KeyboardInterrupt

                previous_handler = runner.signal.signal(signum, interrupt)
                previous_mask = runner.signal.pthread_sigmask(runner.signal.SIG_BLOCK, set())

                def wait(*, timeout):
                    # Signals arrive while each mocked child resists TERM;
                    # cleanup must continue through the bounded KILL wait.
                    if timeout == 10:
                        runner.signal.raise_signal(signum)
                        runner.signal.raise_signal(signum)
                        self.assertEqual(delivered, [])
                        raise runner.subprocess.TimeoutExpired("mock child", timeout)

                launch.check.side_effect = KeyboardInterrupt
                launch.runtime.wait.side_effect = launch.listener.wait.side_effect = wait
                try:
                    with self.assertRaises(KeyboardInterrupt):
                        runner.run(launch.args)
                    self.assertEqual(delivered, [signum])
                    self.assertEqual(launch.killpg.call_args_list, [
                        mock.call(102, runner.signal.SIGTERM), mock.call(102, runner.signal.SIGKILL),
                        mock.call(101, runner.signal.SIGTERM), mock.call(101, runner.signal.SIGKILL),
                    ])
                    for child in (launch.runtime, launch.listener):
                        self.assertEqual(child.wait.call_args_list,
                                         [mock.call(timeout=10), mock.call(timeout=5)])
                    self.assertEqual(runner.signal.pthread_sigmask(runner.signal.SIG_BLOCK, set()),
                                     previous_mask)
                finally:
                    runner.signal.signal(signum, previous_handler)
                    runner.signal.pthread_sigmask(runner.signal.SIG_SETMASK, previous_mask)

    def test_listener_stops_even_if_runtime_kill_wait_fails(self):
        with self.mocked_run() as launch:
            launch.runtime.wait.side_effect = runner.subprocess.TimeoutExpired("mock runtime", 5)
            with self.assertRaises(runner.subprocess.TimeoutExpired):
                runner.run(launch.args)
            self.assertEqual(launch.killpg.call_args_list, [
                mock.call(102, runner.signal.SIGTERM), mock.call(102, runner.signal.SIGKILL),
                mock.call(101, runner.signal.SIGTERM),
            ])
            launch.listener.wait.assert_called_once_with(timeout=10)

    @contextmanager
    def fake_cli(self, mode, marker):
        # Deliberately include synthetic secrets on both output streams and
        # split the Ready prefix across writes. Never invoke the real CLI.
        script = r'''
import os, sys, time
mode, marker = sys.argv[1:]
os.write(1, b"connecting whsec_fake_listener_secret\n")
os.write(2, b"error detail sk_test_fake_cli_secret\n")
if mode == "exit":
    sys.exit(7)
if mode in ("ready", "ready_exit"):
    sys.stdin.buffer.read(1)
    os.write(2, b"\r\x1b[32m> Rea")
    time.sleep(0.05)
    os.write(2, b"dy!\x1b[0m Your webhook signing secret is whsec_fake_listener_secret\n")
    if mode == "ready_exit":
        sys.stdin.buffer.read(1)
        sys.exit(7)
    # More than pipe capacity; the marker proves draining continued after Ready.
    for _ in range(8192):
        os.write(1, b"whsec_fake_listener_secret " * 8 + b"\n")
    open(marker, "w").close()
else:
    os.write(1, b"not > Ready! whsec_fake_listener_secret\n")
time.sleep(30)
'''
        child = subprocess.Popen(
            [sys.executable, "-u", "-c", script, mode, str(marker)],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        try:
            yield child
        finally:
            runner.stop(child)
            child.stdin.close()
            child.stdout.close()

    def assert_redacted(self, text):
        for secret in ("whsec_fake_listener_secret", "sk_test_fake_cli_secret", "whsec_synthetic123"):
            self.assertNotIn(secret, text)

    def test_slow_ready_gates_runtime_and_keeps_draining(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "drained"
            captured = io.StringIO()
            with self.fake_cli("ready", marker) as child, self.mocked_run(child) as launch:
                original_wait = runner.ListenerOutput.wait_ready

                def delayed_ready(output, listener):
                    self.assertFalse(output.ready.is_set())
                    self.assertEqual(launch.popen.call_count, 1)
                    launch.check.assert_not_called()
                    # Release the fake CLI; it still pauses mid-prefix.
                    started = time.monotonic()
                    child.stdin.write(b"x")
                    child.stdin.flush()
                    original_wait(output, listener)
                    self.assertGreaterEqual(time.monotonic() - started, 0.04)
                    self.assertTrue(output.ready.is_set())

                def smoke(*_args):
                    deadline = time.monotonic() + 5
                    while not marker.exists() and time.monotonic() < deadline:
                        time.sleep(0.01)
                    self.assertTrue(marker.exists(), "listener pipe blocked after Ready")

                launch.check.side_effect = smoke
                with (
                    mock.patch.object(runner.ListenerOutput, "wait_ready", delayed_ready),
                    redirect_stdout(captured), redirect_stderr(captured),
                ):
                    self.assertEqual(runner.run(launch.args), 0)
                launch.check.assert_called_once()
                self.assertEqual(launch.popen.call_count, 2)
                listener_options = launch.popen.call_args_list[0].kwargs
                self.assertEqual(listener_options["stdout"], subprocess.PIPE)
                self.assertEqual(listener_options["stderr"], subprocess.STDOUT)
                self.assertIsNotNone(child.poll())
                self.assertTrue(child.stdout.closed)
                self.assert_redacted(captured.getvalue())

    def test_never_ready_and_early_exit_fail_closed_without_secrets(self):
        for mode, message in (("never", "readiness timeout"), ("exit", "exited before readiness")):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                captured = io.StringIO()
                with self.fake_cli(mode, Path(directory) / "unused") as child, self.mocked_run(child) as launch:
                    with (
                        mock.patch.object(runner, "LISTENER_READY_TIMEOUT", 0.3 if mode == "never" else 5),
                        redirect_stdout(captured), redirect_stderr(captured),
                        self.assertRaisesRegex(runner.ListenerReadinessError, message) as caught,
                    ):
                        runner.run(launch.args)
                    self.assertEqual(launch.popen.call_count, 1)
                    launch.check.assert_not_called()
                    self.assertIsNotNone(child.poll())
                    self.assertTrue(child.stdout.closed)
                    self.assert_redacted(captured.getvalue() + str(caught.exception))

    def test_exit_after_ready_stops_runtime_and_fails_startup(self):
        captured = io.StringIO()
        with self.fake_cli("ready_exit", "unused") as child, self.mocked_run(child) as launch:
            child.stdin.write(b"x")
            child.stdin.flush()

            def exit_during_smoke(*_args):
                child.stdin.write(b"x")
                child.stdin.flush()
                child.wait(timeout=5)

            launch.check.side_effect = exit_during_smoke
            with (
                redirect_stdout(captured), redirect_stderr(captured),
                self.assertRaisesRegex(runner.ListenerReadinessError, "stopped during startup") as caught,
            ):
                runner.run(launch.args)
            launch.killpg.assert_called_once_with(launch.runtime.pid, runner.signal.SIGTERM)
            launch.runtime.wait.assert_called_once_with(timeout=10)
            self.assertIsNotNone(child.poll())
            self.assert_redacted(captured.getvalue() + str(caught.exception))

    def test_main_reports_secret_free_readiness_errors(self):
        for message in ("Stripe listener readiness timeout", "Stripe listener exited before readiness"):
            captured = io.StringIO()
            with (
                mock.patch.object(runner.argparse.ArgumentParser, "parse_args"),
                mock.patch.object(runner.signal, "signal"),
                mock.patch.object(runner, "run", side_effect=runner.ListenerReadinessError(message)),
                redirect_stdout(captured), redirect_stderr(captured),
            ):
                self.assertEqual(runner.main(), 1)
            self.assertEqual(captured.getvalue(), message + "\n")
            self.assert_redacted(captured.getvalue())


if __name__ == "__main__":
    unittest.main()
