#!/usr/bin/env python3
"""Supervise the approved loopback Sandbox runtime without logging secrets.

Requires an already installed official Stripe CLI, built web assets, a measured
authorized runner, secure process environment, and disposable local PostgreSQL.
Does not install software, source .env files, log in, or change API versions.
"""

import argparse
import json
import socket
import urllib.error
import urllib.request
import os
from pathlib import Path
import re
import shutil
import getpass
import signal
import subprocess
import sys
import time
import warnings


EVENTS = ",".join((
    "checkout.session.completed", "checkout.session.async_payment_succeeded",
    "checkout.session.async_payment_failed", "checkout.session.expired",
    "payment_intent.succeeded", "payment_intent.payment_failed",
    "payment_intent.processing",
))
API_VERSION = "2026-09-30.endive"
MODE = "CONNECTED_DIRECT_TEST_FIXTURE"
PLATFORM_ACCOUNT = "acct_1URlHu1Hm4CZe205"


def checked_environment(source):
    """Keyless-testable admission; never embed a supplied value in an error."""
    env = dict(source)
    if env.get("TOJIMA_STRIPE_MODE") != MODE or env.get("TOJIMA_STRIPE_API_VERSION") != API_VERSION:
        raise ValueError("invalid test mode/API version")
    key = env.get("TOJIMA_STRIPE_TEST_SECRET_KEY", "")
    if not key.startswith("sk_test_") or len(key) < 16 or any(c.isspace() or ord(c) < 32 for c in key):
        raise ValueError("missing or invalid Sandbox test key")
    accounts = [env.get("TOJIMA_STRIPE_TEST_ACCOUNT_" + tenant, "") for tenant in "ABC"]
    if any(not re.fullmatch(r"acct_[A-Za-z0-9]+", a) or a == PLATFORM_ACCOUNT for a in accounts) or len(set(accounts)) != 3:
        raise ValueError("invalid synthetic connected account mapping")
    amount = env.get("TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "")
    if not re.fullmatch(r"[1-9][0-9]*", amount) or len(amount) > 19 or int(amount) > 2**63 - 1:
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


def startup_check(runtime, listener, credentials_path, tenant, port):
    endpoint = f"http://127.0.0.1:{port}"
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        if runtime.poll() is not None or listener.poll() is not None:
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
    if runtime.poll() is not None or listener.poll() is not None:
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


def run(args):
    source = dict(os.environ)
    if args.prompt_key:
        if not sys.stdin.isatty():
            raise ValueError("secure interactive key entry requires a terminal")
        source["TOJIMA_STRIPE_TEST_SECRET_KEY"] = hidden_key_input()
    env = checked_environment(source)
    root = Path(__file__).resolve().parents[1]
    cli = shutil.which("stripe")
    if cli is None or not (root / "web/dist/index.html").is_file():
        raise ValueError("official Stripe CLI and built web assets are required")
    if not 1024 <= args.port <= 65535:
        raise ValueError("invalid loopback port")
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", args.port))
    # Never persist Stripe CLI login or put the key in argv. Capture the CLI's
    # signing-secret output into memory; do not relay stdout/stderr on errors.
    cli_env = dict(env)
    cli_env["STRIPE_API_KEY"] = env["TOJIMA_STRIPE_TEST_SECRET_KEY"]
    cli_env["STRIPE_DEVICE_NAME"] = "tojima-isolated-sandbox"
    version = subprocess.run([cli, "version"], env=cli_env,
                             stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                             stderr=subprocess.DEVNULL, timeout=5, check=False)
    if version.returncode != 0 or version.stdout.strip() != b"stripe version 1.54.0":
        raise ValueError("official pinned Stripe CLI 1.54.0 is required")
    result = subprocess.run([cli, "listen", "--print-secret"], env=cli_env,
                            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, timeout=30, check=False)
    secret = result.stdout.decode("ascii").rstrip("\r\n")
    if result.returncode != 0 or len(result.stdout) > 4096 or not re.fullmatch(r"whsec_[A-Za-z0-9]{8,}", secret):
        raise ValueError("Stripe CLI could not prepare its local signing secret")
    env["TOJIMA_STRIPE_TEST_WEBHOOK_SECRET"] = secret
    # The normal credential document contains local staff fixtures only.
    # Go's configuration deliberately keeps the Stripe key/secret out of it.
    endpoint = f"http://127.0.0.1:{args.port}/v1/payments/stripe/webhook"
    command = [str(Path(args.runtime_binary).resolve())]
    if args.runtime_binary and not Path(args.runtime_binary).is_file():
        raise ValueError("prebuilt runtime binary is missing")
    listener = runtime = None
    try:
        listener = subprocess.Popen(
            [cli, "listen", "--forward-connect-to", endpoint, "--events", EVENTS,
             "--skip-update"], cwd=root, env=cli_env, stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            start_new_session=True)
        runtime = subprocess.Popen(
            command + ["-stripe-sandbox", "-stripe-tenant", args.tenant,
             "-port", str(args.port), "-credentials", str(Path(args.credentials).resolve())],
            cwd=root, env=env, stdin=subprocess.DEVNULL, start_new_session=True)
        print("Isolated Sandbox runtime starting; Stripe CLI output is suppressed", flush=True)
        startup_check(runtime, listener, args.credentials, args.tenant, args.port)
        if args.startup_check:
            return 0
        while runtime.poll() is None and listener.poll() is None:
            time.sleep(0.5)
        if runtime.poll() is not None:
            return runtime.returncode
        print("Stripe CLI listener stopped; stopping the isolated runtime", file=sys.stderr)
        return 1
    finally:
        stop(runtime)
        stop(listener)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tenant", choices=list("ABC"), required=True)
    parser.add_argument("--port", type=int, default=8083)
    parser.add_argument("--prompt-key", action="store_true", help="user enters test key through a hidden terminal prompt; never saved")
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
    except (ValueError, OSError, subprocess.SubprocessError):
        print("Sandbox startup failed; verify the documented secure environment and prerequisites", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
