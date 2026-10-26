"""Read and validate the approved Stripe Sandbox test key from macOS Keychain."""

import subprocess


DEFAULT_KEYCHAIN_SERVICE = "tojima-stripe-sandbox"


def keychain_key(service=DEFAULT_KEYCHAIN_SERVICE):
    try:
        result = subprocess.run(
            ["security", "find-generic-password", "-s", service, "-w"],
            capture_output=True,
            text=True,
            check=False,
        )
    except OSError:
        raise ValueError("macOS Keychain security command is unavailable") from None
    key = result.stdout.strip()
    if result.returncode != 0:
        raise ValueError(f"Keychain item '{service}' is missing or inaccessible")
    if not key.startswith("sk_test_") or any(c.isspace() or ord(c) < 32 for c in key):
        raise ValueError(f"Keychain item '{service}' does not contain a valid sk_test_ key")
    return key
