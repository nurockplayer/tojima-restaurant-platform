#!/usr/bin/env python3
"""Provision synthetic Sandbox merchants A/B/C through Stripe Accounts v2.

The accounts are platform-controlled (the platform collects requirements and
carries fees/losses, no Stripe Dashboard for the merchant), so no merchant
login, 2FA or hosted onboarding is needed. Every field is Stripe test data.

The test key is read from the macOS Keychain item `tojima-stripe-sandbox`
(the Owner stores it once with `security add-generic-password -s
tojima-stripe-sandbox -a sk_test -w`). It is never printed or written to disk.
The script refuses any key that is not `sk_test_` or that does not belong to
the approved Sandbox platform account.

Output: account IDs (not secrets) as TOJIMA_STRIPE_TEST_ACCOUNT_{A,B,C} lines,
plus each account's card_payments status and remaining requirements.
"""

import argparse
import base64
import datetime
import json
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

PLATFORM_ACCOUNT = "acct_1URlHu1Hm4CZe205"
API_VERSION = "2026-09-30.endive"
KEYCHAIN_SERVICE = "tojima-stripe-sandbox"
FIXTURE_KEY = "tojima_test_fixture"
TENANTS = "ABC"
INCLUDE = ["configuration.merchant", "requirements", "identity", "defaults"]


def keychain_key():
    out = subprocess.run(
        ["security", "find-generic-password", "-s", KEYCHAIN_SERVICE, "-w"],
        capture_output=True, text=True,
    )
    key = out.stdout.strip()
    if out.returncode != 0 or not key.startswith("sk_test_") or any(c.isspace() for c in key):
        raise SystemExit(f"Keychain item '{KEYCHAIN_SERVICE}' missing or not an sk_test_ key")
    return key


def form(data, prefix=""):
    pairs = []
    for name, value in data.items():
        field = f"{prefix}[{name}]" if prefix else name
        if isinstance(value, dict):
            pairs += form(value, field)
        else:
            pairs.append((field, str(value)))
    return pairs


class Stripe:
    def __init__(self, key):
        self.auth = "Basic " + base64.b64encode(f"{key}:".encode()).decode()

    def call(self, method, path, data=None):
        req = urllib.request.Request("https://api.stripe.com" + path, method=method)
        req.add_header("Authorization", self.auth)
        req.add_header("Stripe-Version", API_VERSION)
        if data is not None and path.startswith("/v2/"):
            req.data = json.dumps(data, ensure_ascii=False).encode()
            req.add_header("Content-Type", "application/json")
        elif data is not None:
            req.data = urllib.parse.urlencode(form(data)).encode()
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                return json.load(resp)
        except urllib.error.HTTPError as err:
            detail = json.load(err).get("error", {})
            raise SystemExit(f"Stripe {method} {path.split('?')[0]} failed: "
                             f"{detail.get('type')} {detail.get('code')} {detail.get('param')} "
                             f"{detail.get('message')}") from None


def label(tenant):
    return f"synthetic_merchant_{tenant.lower()}_api"


def account_body(tenant):
    email = f"tojima-merchant-{tenant.lower()}@example.com"
    now = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
    kana = {"country": "jp", "postal_code": "1500001", "state": "ﾄｳｷﾖｳﾄ", "city": "ｼﾌﾞﾔｸ",
            "town": "ｼﾞﾝｸﾞｳﾏｴ 1-", "line1": "1-1"}
    kanji = {"country": "jp", "postal_code": "1500001", "state": "東京都", "city": "渋谷区",
             "town": "神宮前　１丁目", "line1": "１－１"}
    return {
        "contact_email": email,
        "display_name": f"Tojima Test Merchant {tenant}",
        "dashboard": "none",
        "metadata": {FIXTURE_KEY: label(tenant)},
        "identity": {
            "country": "jp",
            "entity_type": "individual",
            "attestations": {"terms_of_service": {"account": {"date": now, "ip": "127.0.0.1"}}},
            "individual": {
                "given_name": "Taro", "surname": "Test",
                "script_names": {"kana": {"given_name": "タロウ", "surname": "テスト"},
                                 "kanji": {"given_name": "太郎", "surname": "テスト"}},
                "date_of_birth": {"day": 1, "month": 1, "year": 1901},
                "email": email,
                "phone": "+81312345678",
                "script_addresses": {"kana": kana, "kanji": kanji},
            },
            "business_details": {"phone": "+81312345678"},
        },
        "configuration": {"merchant": {
            "capabilities": {"card_payments": {"requested": True}},
            "mcc": "5812",
            "statement_descriptor": {"descriptor": f"TOJIMA TEST {tenant}"},
            "script_statement_descriptor": {"kana": {"descriptor": "トウジマテスト"},
                                            "kanji": {"descriptor": "東島テスト"}},
            "support": {"phone": "+81312345678", "email": email},
        }},
        "defaults": {
            "currency": "jpy",
            "responsibilities": {"fees_collector": "application", "losses_collector": "application"},
            "profile": {"business_url": "https://accessible.stripe.com",
                        "doing_business_as": f"Tojima Test {tenant}",
                        "product_description": "Synthetic restaurant used only for Sandbox tests"},
        },
        "include": INCLUDE,
    }


def create_account(stripe, tenant):
    # Stripe returns a 500 when the full body is sent in one create (seen
    # 2026-10-26 JST), so create the minimal account and apply the rest in parts.
    full = account_body(tenant)
    ind = full["identity"]["individual"]
    minimal = {k: full[k] for k in ("contact_email", "display_name", "dashboard", "metadata", "include")}
    minimal["identity"] = {"country": "jp", "entity_type": "individual"}
    minimal["defaults"] = {"currency": "jpy", "responsibilities": full["defaults"]["responsibilities"]}
    minimal["configuration"] = {"merchant": {"capabilities": {"card_payments": {"requested": True}}}}
    acct_id = stripe.call("POST", "/v2/core/accounts", minimal)["id"]
    merchant = {k: v for k, v in full["configuration"]["merchant"].items() if k != "capabilities"}
    for part in (
        {"defaults": {"profile": full["defaults"]["profile"]}},
        {"configuration": {"merchant": merchant}},
        {"identity": {"individual": {k: v for k, v in ind.items() if k != "script_addresses"}}},
        {"identity": {"individual": {"script_addresses": ind["script_addresses"]}}},
        {"identity": {"business_details": full["identity"]["business_details"]}},
        {"identity": {"attestations": full["identity"]["attestations"]}},
        # Stripe's magic test file that always passes document verification.
        {"identity": {"individual": {"documents": {"primary_verification": {
            "type": "front_back", "front_back": {"front": "file_identity_document_success"}}}}}},
    ):
        stripe.call("POST", f"/v2/core/accounts/{acct_id}", part)
    return acct_id


def find_existing(stripe):
    found, page = {}, stripe.call("GET", "/v2/core/accounts?limit=20")
    while True:
        for acct in page.get("data", []):
            for tenant in TENANTS:
                if (acct.get("metadata") or {}).get(FIXTURE_KEY) == label(tenant):
                    found.setdefault(tenant, acct["id"])
        nxt = page.get("next_page_url")
        if not nxt:
            return found
        page = stripe.call("GET", nxt)


def ensure_bank_account(stripe, acct_id):
    existing = stripe.call("GET", f"/v1/accounts/{acct_id}/external_accounts?object=bank_account&limit=1")
    if existing.get("data"):
        return
    stripe.call("POST", f"/v1/accounts/{acct_id}/external_accounts", {"external_account": {
        "object": "bank_account", "country": "JP", "currency": "jpy",
        "routing_number": "1100000", "account_number": "0001234",
        "account_holder_name": "ﾃｽﾄ ﾀﾛｳ",
    }})


def report(stripe, tenant, acct_id):
    query = urllib.parse.urlencode([("include", i) for i in INCLUDE])
    acct = stripe.call("GET", f"/v2/core/accounts/{acct_id}?{query}")
    caps = (((acct.get("configuration") or {}).get("merchant") or {}).get("capabilities") or {})
    card = (caps.get("card_payments") or {}).get("status")
    entries = ((acct.get("requirements") or {}).get("entries") or [])
    due = sorted({e.get("description") or e.get("awaiting_action_from") or json.dumps(e)[:80]
                  for e in entries if (e.get("minimum_deadline") or {}).get("status") in ("currently_due", "past_due")})
    print(f"TOJIMA_STRIPE_TEST_ACCOUNT_{tenant}={acct_id}")
    print(f"# {tenant}: card_payments={card} due={due}")
    return card == "active"


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--status", action="store_true", help="only report existing accounts")
    args = parser.parse_args()
    stripe = Stripe(keychain_key())
    if stripe.call("GET", "/v1/account").get("id") != PLATFORM_ACCOUNT:
        raise SystemExit("key does not belong to the approved Sandbox platform account")
    accounts = find_existing(stripe)
    for tenant in TENANTS:
        if args.status:
            continue
        if tenant not in accounts:
            accounts[tenant] = create_account(stripe, tenant)
        ensure_bank_account(stripe, accounts[tenant])
    if not args.status:
        time.sleep(5)
    ok = True
    for tenant in TENANTS:
        if tenant not in accounts:
            print(f"# {tenant}: not created")
            ok = False
        else:
            ok = report(stripe, tenant, accounts[tenant]) and ok
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
