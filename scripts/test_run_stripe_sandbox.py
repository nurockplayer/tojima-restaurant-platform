"""Keyless tests only: no subprocesses, network, credentials or provider calls."""
import importlib.util
from contextlib import contextmanager
from pathlib import Path
from types import SimpleNamespace
import re
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
    def mocked_run(self):
        source = dict(self.fixture(), PATH="/usr/bin", HOME="/test/home",
                      STRIPE_API_KEY="sk_test_inherited",
                      STRIPE_WEBHOOK_SECRET="whsec_inherited",
                      TOJIMA_STRIPE_TEST_WEBHOOK_SECRET="whsec_stale",
                      UNRELATED_SECRET="unrelated")
        listener = mock.Mock(pid=101)
        runtime = mock.Mock(pid=102)
        listener.poll.return_value = runtime.poll.return_value = None
        args = SimpleNamespace(prompt_key=False, port=8083, tenant="A",
                               runtime_binary="/test/runtime", credentials="/test/credentials",
                               startup_check=True)
        with (
            mock.patch.dict(runner.os.environ, source, clear=True),
            mock.patch.object(runner.shutil, "which", return_value="/test/stripe"),
            mock.patch.object(runner.Path, "is_file", return_value=True),
            mock.patch.object(runner.socket, "socket"),
            mock.patch.object(runner.subprocess, "run", side_effect=[
                SimpleNamespace(returncode=0, stdout=b"stripe version 1.54.0\n"),
                SimpleNamespace(returncode=0, stdout=b"whsec_synthetic123\n"),
            ]) as cli,
            mock.patch.object(runner.subprocess, "Popen", side_effect=[listener, runtime]) as popen,
            mock.patch.object(runner, "startup_check") as check,
            mock.patch.object(runner.os, "killpg") as killpg,
            mock.patch("builtins.print"),
        ):
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


if __name__ == "__main__":
    unittest.main()
