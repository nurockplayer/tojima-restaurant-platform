#!/usr/bin/env python3
"""Supervise the approved loopback Sandbox runtime without logging secrets.

Requires an already installed official Stripe CLI, built web assets, a measured
authorized runner, secure process environment, and disposable local PostgreSQL.
Does not install software, source .env files, log in, or change API versions.
"""

import argparse
import base64
import json
import socket
import urllib.error
import urllib.parse
import urllib.request
import os
from pathlib import Path
import re
import shutil
import getpass
import signal
import subprocess
import sys
import threading
import time
import warnings

from stripe_sandbox_keychain import DEFAULT_KEYCHAIN_SERVICE, OPENER, keychain_key


EVENTS = ",".join((
    "checkout.session.completed", "checkout.session.async_payment_succeeded",
    "checkout.session.async_payment_failed", "checkout.session.expired",
    "payment_intent.succeeded", "payment_intent.payment_failed",
    "payment_intent.processing",
    "refund.updated", "refund.failed", "charge.refunded",
))
API_VERSION = "2026-09-30.endive"
MODE = "CONNECTED_DIRECT_TEST_FIXTURE"
PLATFORM_ACCOUNT = "acct_1URlHu1Hm4CZe205"
DEFAULT_MERCHANT_ACCOUNTS = {
    "A": "acct_1UUXcL1Hm4VOReK9",
    "B": "acct_1UUXd51Hm4v7xrOu",
    "C": "acct_1UUXdU1Hm4vaUfKX",
}
LISTENER_READY_TIMEOUT = 120


class ListenerReadinessError(ValueError):
    """Only fixed, secret-free readiness diagnostics may use this error."""


class ListenerOutput:
    """Discard bytes, retaining only readiness/EOF flags and parser state.

    Never assemble a line: the Ready line itself contains the signing secret.
    Both CLI streams share this pipe; keep draining after readiness as well.
    """

    def __init__(self, stream):
        self.ready = threading.Event()
        self.ended = threading.Event()
        self.thread = threading.Thread(target=self._drain, args=(stream,), daemon=True)
        self.thread.start()

    def _drain(self, stream):
        prefix = b"> Ready! "
        matched = 0
        escape = 0
        try:
            while byte := stream.read(1):
                # Ignore ANSI CSI styling without retaining escape text.
                if escape:
                    if escape == 1:
                        escape = 2 if byte == b"[" else 0
                    elif b"@" <= byte <= b"~":
                        escape = 0
                    continue
                if byte == b"\x1b":
                    escape = 1
                elif byte in (b"\r", b"\n"):
                    matched = 0
                elif matched >= 0:
                    if byte[0] != prefix[matched]:
                        matched = -1
                    else:
                        matched += 1
                        if matched == len(prefix):
                            self.ready.set()
                            matched = -1
        except (OSError, ValueError):
            # No exception text or pipe content may escape the reader.
            pass
        finally:
            self.ended.set()

    def wait_ready(self, listener):
        deadline = time.monotonic() + LISTENER_READY_TIMEOUT
        while True:
            if listener.poll() is not None or self.ended.is_set():
                raise ListenerReadinessError("Stripe listener exited before readiness")
            if self.ready.is_set():
                return
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise ListenerReadinessError("Stripe listener readiness timeout")
            self.ready.wait(min(0.05, remaining))


def checked_environment(source):
    """Keyless-testable admission; never embed a supplied value in an error."""
    env = dict(source)
    for tenant, account in DEFAULT_MERCHANT_ACCOUNTS.items():
        env.setdefault("TOJIMA_STRIPE_TEST_ACCOUNT_" + tenant, account)
    if env.get("TOJIMA_STRIPE_MODE") != MODE or env.get("TOJIMA_STRIPE_API_VERSION") != API_VERSION:
        raise ValueError("invalid test mode/API version")
    key = env.get("TOJIMA_STRIPE_TEST_SECRET_KEY", "")
    if not key.startswith("sk_test_") or len(key) < 16 or any(c.isspace() or ord(c) < 32 for c in key):
        raise ValueError("missing or invalid Sandbox test key")
    accounts = [env.get("TOJIMA_STRIPE_TEST_ACCOUNT_" + tenant, "") for tenant in "ABC"]
    if any(not re.fullmatch(r"acct_[A-Za-z0-9]+", a) or a == PLATFORM_ACCOUNT for a in accounts) or len(set(accounts)) != 3:
        raise ValueError("invalid synthetic connected account mapping")
    amount = env.get("TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "")
    if not re.fullmatch(r"[1-9][0-9]*", amount) or len(amount) > 19 or int(amount) > 10000:
        raise ValueError("invalid test JPY ceiling")
    # The existing Go fixture parses and enforces disposable loopback DB scope.
    if not env.get("TEST_DATABASE_URL"):
        raise ValueError("disposable TEST_DATABASE_URL is required")
    return env


def stop(child):
    if child is None or child.poll() is not None:
        return
    try:
        os.killpg(child.pid, signal.SIGTERM)
        child.wait(timeout=10)
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGKILL)
        child.wait(timeout=5)
    except ProcessLookupError:
        pass


def startup_check(runtime, listener, credentials_path, tenant, port, listener_output):
    endpoint = f"http://127.0.0.1:{port}"
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        if runtime.poll() is not None or listener.poll() is not None or listener_output.ended.is_set():
            raise ValueError("runtime/listener exited before readiness")
        try:
            with urllib.request.urlopen(endpoint + "/healthz", timeout=2) as response:
                ready = response.status == 200
            if ready and Path(credentials_path).is_file():
                with open(credentials_path, encoding="utf-8") as f:
                    document = json.load(f)
                if (document.get("stripe_sandbox") is not True or
                    document.get("tenant_id") != "stripe-test-tenant-" + tenant.lower() or
                    "webhook_fixture_secret" in document):
                    raise ValueError("invalid runtime credential document")
                break
        except (urllib.error.URLError, OSError, json.JSONDecodeError):
            time.sleep(0.5)
    else:
        raise ValueError("runtime startup timeout")
    for request, expected in (
        (urllib.request.Request(endpoint + "/healthz", headers={"Host": "attacker.invalid"}), 403),
        (urllib.request.Request(endpoint + "/v1/payments/stripe/webhook", data=b"{}", method="POST"), 400),
    ):
        try:
            urllib.request.urlopen(request, timeout=5).close()
            raise ValueError("unsafe request was accepted")
        except urllib.error.HTTPError as response:
            if response.code != expected:
                raise ValueError("unexpected HTTP guard status") from None
    if runtime.poll() is not None or listener.poll() is not None or listener_output.ended.is_set():
        raise ValueError("runtime/listener did not remain active")
    print("Sandbox startup smoke PASS: exact Host and unsigned-webhook guards; no payment E2E claim", flush=True)


def hidden_key_input():
    # getpass may otherwise warn and fall back to echoed input even on a TTY.
    # Turn that warning into an error before fallback can read any key bytes.
    with warnings.catch_warnings():
        warnings.simplefilter("error", getpass.GetPassWarning)
        try:
            return getpass.getpass("Stripe Sandbox test key (hidden): ")
        except getpass.GetPassWarning:
            raise ValueError("secure terminal echo control is unavailable") from None


def verify_platform_account(key):
    """Fail closed unless the supplied test key belongs to the approved account."""
    request = urllib.request.Request("https://api.stripe.com/v1/account")
    request.add_header("Authorization", "Basic " + base64.b64encode((key + ":").encode()).decode())
    request.add_header("Stripe-Version", API_VERSION)
    try:
        with OPENER.open(request, timeout=15) as response:
            account = json.load(response)
    except (OSError, urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError):
        raise ValueError("could not verify Stripe Sandbox platform account") from None
    if not isinstance(account, dict) or account.get("id") != PLATFORM_ACCOUNT:
        raise ValueError("key does not belong to the approved Stripe Sandbox platform account")


def verify_merchant_accounts(key, env):
    for tenant in "ABC":
        account_id = env["TOJIMA_STRIPE_TEST_ACCOUNT_" + tenant]
        query = urllib.parse.urlencode([("include", "configuration.merchant"), ("include", "defaults")])
        request = urllib.request.Request(
            f"https://api.stripe.com/v2/core/accounts/{account_id}?{query}")
        request.add_header("Authorization", "Basic " + base64.b64encode((key + ":").encode()).decode())
        request.add_header("Stripe-Version", API_VERSION)
        try:
            with OPENER.open(request, timeout=15) as response:
                account = json.load(response)
        except (OSError, urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError):
            raise ValueError(f"could not verify Sandbox merchant {tenant}") from None
        metadata = account.get("metadata") if isinstance(account, dict) else None
        defaults = account.get("defaults") if isinstance(account, dict) else None
        capabilities = (((account.get("configuration") or {}).get("merchant") or {}).get("capabilities") or {}) if isinstance(account, dict) else {}
        if (not isinstance(metadata, dict) or metadata.get("tojima_test_fixture") !=
                f"synthetic_merchant_{tenant.lower()}_api" or
                not isinstance(defaults, dict) or defaults.get("currency") != "jpy" or
                (capabilities.get("card_payments") or {}).get("status") != "active"):
            raise ValueError(f"merchant {tenant} is not the approved Sandbox fixture account")


def run(args):
    source = dict(os.environ)
    if args.prompt_key:
        if not sys.stdin.isatty():
            raise ValueError("secure interactive key entry requires a terminal")
        source["TOJIMA_STRIPE_TEST_SECRET_KEY"] = hidden_key_input()
    else:
        source["TOJIMA_STRIPE_TEST_SECRET_KEY"] = keychain_key(args.keychain_service)
    env = checked_environment(source)
    verify_platform_account(env["TOJIMA_STRIPE_TEST_SECRET_KEY"])
    verify_merchant_accounts(env["TOJIMA_STRIPE_TEST_SECRET_KEY"], env)
    root = Path(__file__).resolve().parents[1]
    cli = shutil.which("stripe")
    if cli is None or not (root / "web/dist/index.html").is_file():
        raise ValueError("official Stripe CLI and built web assets are required")
    if not 1024 <= args.port <= 65535:
        raise ValueError("invalid loopback port")
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", args.port))
    # Admit the CLI version without credentials or inherited Stripe settings.
    base_env = {name: env[name] for name in (
        "PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL",
        "SSL_CERT_FILE", "SSL_CERT_DIR",
    ) if name in env}
    version = subprocess.run([cli, "version"], env=base_env,
                             stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                             stderr=subprocess.DEVNULL, timeout=5, check=False)
    if version.returncode != 0 or version.stdout.strip() != b"stripe version 1.54.0":
        raise ValueError("official pinned Stripe CLI 1.54.0 is required")
    # Never persist Stripe CLI login or put the key in argv. Capture the CLI's
    # signing-secret output into memory; do not relay stdout/stderr on errors.
    cli_env = dict(base_env, STRIPE_API_KEY=env["TOJIMA_STRIPE_TEST_SECRET_KEY"],
                   STRIPE_DEVICE_NAME="tojima-isolated-sandbox")
    result = subprocess.run([cli, "listen", "--print-secret"], env=cli_env,
                            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, timeout=30, check=False)
    secret = result.stdout.decode("ascii").rstrip("\r\n")
    if result.returncode != 0 or len(result.stdout) > 4096 or not re.fullmatch(r"whsec_[A-Za-z0-9]{8,}", secret):
        raise ValueError("Stripe CLI could not prepare its local signing secret")
    runtime_env = dict(base_env)
    for name in (
        "TEST_DATABASE_URL", "TOJIMA_STRIPE_MODE", "TOJIMA_STRIPE_API_VERSION",
        "TOJIMA_STRIPE_TEST_SECRET_KEY", "TOJIMA_STRIPE_TEST_ACCOUNT_A",
        "TOJIMA_STRIPE_TEST_ACCOUNT_B", "TOJIMA_STRIPE_TEST_ACCOUNT_C",
        "TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY",
    ):
        runtime_env[name] = env[name]
    runtime_env["TOJIMA_STRIPE_TEST_WEBHOOK_SECRET"] = secret
    # The normal credential document contains local staff fixtures only.
    # Go's configuration deliberately keeps the Stripe key/secret out of it.
    endpoint = f"http://127.0.0.1:{args.port}/v1/payments/stripe/webhook"
    command = [str(Path(args.runtime_binary).resolve())]
    if args.runtime_binary and not Path(args.runtime_binary).is_file():
        raise ValueError("prebuilt runtime binary is missing")
    listener = runtime = output = None
    try:
        listener = subprocess.Popen(
            [cli, "listen", "--forward-connect-to", endpoint, "--events", EVENTS,
             "--skip-update"], cwd=root, env=cli_env, stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            start_new_session=True)
        output = ListenerOutput(listener.stdout)
        output.wait_ready(listener)
        # Go announces its URL on launch, so do not start it before forwarding
        # is ready. No listener output is relayed, including on failure.
        runtime = subprocess.Popen(
            command + ["-stripe-sandbox", "-stripe-tenant", args.tenant,
             "-port", str(args.port), "-credentials", str(Path(args.credentials).resolve())],
            cwd=root, env=runtime_env, stdin=subprocess.DEVNULL, start_new_session=True)
        print("Isolated Sandbox runtime starting; Stripe CLI output is suppressed", flush=True)
        startup_check(runtime, listener, args.credentials, args.tenant, args.port, output)
        if listener.poll() is not None or output.ended.is_set():
            raise ListenerReadinessError("Stripe listener stopped during startup")
        if args.startup_check:
            return 0
        while runtime.poll() is None and listener.poll() is None and not output.ended.is_set():
            time.sleep(0.5)
        if runtime.poll() is not None:
            return runtime.returncode
        print("Stripe CLI listener stopped; stopping the isolated runtime", file=sys.stderr)
        return 1
    finally:
        # Defer repeated termination signals until both bounded group stops
        # finish. Restore the caller's mask, delivering any pending signal.
        previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            try:
                stop(runtime)
            finally:
                try:
                    stop(listener)
                finally:
                    if output is not None:
                        output.thread.join(timeout=1)
                    if listener is not None and (output is None or not output.thread.is_alive()):
                        listener.stdout.close()
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tenant", choices=list("ABC"), required=True)
    parser.add_argument("--port", type=int, default=8083)
    parser.add_argument("--keychain-service", default=DEFAULT_KEYCHAIN_SERVICE,
                        help="macOS Keychain generic-password service for the Sandbox test key")
    parser.add_argument("--prompt-key", action="store_true", help="use an explicit hidden terminal prompt instead of Keychain")
    parser.add_argument("--runtime-binary", required=True, help="already built operationstest binary; avoids compilation with credentials")
    parser.add_argument("--startup-check", action="store_true", help="check runtime/HTTP guards then stop; does not exercise Checkout")
    parser.add_argument("--credentials", required=True, help="new owner-only local staff credential file")
    args = parser.parse_args()
    # Avoid tracebacks carrying subprocess arguments, env dictionaries or output.
    def terminate(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, terminate)
    try:
        return run(args)
    except KeyboardInterrupt:
        return 130
    except ListenerReadinessError as error:
        print(str(error), file=sys.stderr)
        return 1
    except ValueError as error:
        print(str(error), file=sys.stderr)
        return 1
    except (OSError, subprocess.SubprocessError):
        print("Sandbox startup failed; verify the documented secure environment and prerequisites", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
