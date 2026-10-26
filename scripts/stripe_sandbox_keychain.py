"""Read and validate the approved Stripe Sandbox test key from macOS Keychain."""

import subprocess
import re
import urllib.error
import urllib.request


DEFAULT_KEYCHAIN_SERVICE = "tojima-stripe-sandbox"
_SECRET = re.compile(r"\b(?:sk|rk|pk|whsec)_(?:test|live)?_?[A-Za-z0-9*]+")


def redact(text):
    return _SECRET.sub("[redacted]", str(text))


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.HTTPError(req.full_url, code, "redirect refused for authenticated Stripe request", headers, fp)


OPENER = urllib.request.build_opener(_NoRedirect)


def keychain_key(service=DEFAULT_KEYCHAIN_SERVICE, account="sk_test"):
    try:
        result = subprocess.run(
            ["security", "find-generic-password", "-s", service, "-a", account, "-w"],
            capture_output=True,
            text=True,
            check=False,
            timeout=10,
        )
    except subprocess.TimeoutExpired:
        raise ValueError(f"Keychain item '{service}' lookup timed out") from None
    except OSError:
        raise ValueError("macOS Keychain security command is unavailable") from None
    key = result.stdout.strip()
    if result.returncode != 0:
        raise ValueError(f"Keychain item '{service}' is missing or inaccessible")
    if not key.startswith("sk_test_") or any(c.isspace() or ord(c) < 32 for c in key):
        raise ValueError(f"Keychain item '{service}' does not contain a valid sk_test_ key")
    return key
