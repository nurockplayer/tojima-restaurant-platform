"""Keyless tests only: no subprocesses, network, credentials or provider calls."""
import importlib.util
from pathlib import Path
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


if __name__ == "__main__":
    unittest.main()
