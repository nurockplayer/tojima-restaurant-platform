#!/bin/sh
# Disposable real-Keycloak password/OTP acceptance (ADR-0008 F1).
# Run only under orb-test --heavy --host; never use real .env or credentials.
set -eu
smoke_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
smoke_project="tojima-staff-accept-$$"
umask 077
smoke_tmp=$(mktemp -d)
smoke_docker_checked=false
compose() {
    docker compose -f "$smoke_root/compose.yaml" --env-file "$smoke_tmp/.env" -p "$smoke_project" --profile staff "$@"
}
cleanup() {
    smoke_status=$?
    trap - EXIT HUP INT TERM
    if ! compose down -v --rmi local --remove-orphans >/dev/null 2>&1; then
        echo 'cleanup: docker compose down failed' >&2
        smoke_status=1
    fi
    smoke_left_containers=$(compose ps -aq 2>/dev/null) || { smoke_left_containers=; smoke_status=1; }
    smoke_left_volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$smoke_project" 2>/dev/null) || { smoke_left_volumes=; smoke_status=1; }
    smoke_left_images=$(docker images -q --filter "reference=${smoke_project}-*" 2>/dev/null) || { smoke_left_images=; smoke_status=1; }
    for smoke_left in "$smoke_left_containers" "$smoke_left_volumes" "$smoke_left_images"; do
        if [ -n "$smoke_left" ]; then echo "cleanup: resources left behind: $smoke_left" >&2; smoke_status=1; fi
    done
    if [ "$smoke_docker_checked" = true ]; then
        smoke_dangling_after=$(docker volume ls -qf dangling=true) || smoke_status=1
        echo "dangling volumes after: ${smoke_dangling_after:-none}"
        # Report new unlabeled/anonymous leftovers too, preserving pre-existing
        # volumes and never pruning another project on this shared host.
        for smoke_volume in $smoke_dangling_after; do
            smoke_known=false
            for smoke_before in $smoke_dangling_before; do
                if [ "$smoke_volume" = "$smoke_before" ]; then smoke_known=true; fi
            done
            if [ "$smoke_known" = false ]; then
                echo "cleanup: new dangling volume: $smoke_volume" >&2
                smoke_status=1
            fi
        done
    fi
    rm -rf "$smoke_tmp"
    exit "$smoke_status"
}
trap cleanup EXIT HUP INT TERM
fail() { echo "RESULT: FAIL $*" >&2; exit 1; }
for smoke_tool in docker curl python3 openssl; do command -v "$smoke_tool" >/dev/null 2>&1 || fail "$smoke_tool is required"; done
# Environment overrides --env-file. Clear every Compose interpolation input,
# retaining Docker's own connection/context environment.
for smoke_key in POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD APP_DB_USER APP_DB_PASSWORD KEYCLOAK_DB_PASSWORD APP_BIND APP_PORT \
    SESSION_TTL GRANT_TTL GRANT_WINDOW GRANT_WINDOW_MAX PIN_TTL PIN_LOCKOUT PIN_MAX_ATTEMPTS PIN_LENGTH PIN_PEPPER \
    PAYMENT_CREATE_RETRY_WINDOW PAYMENT_CONTINUATION_TTL PAYMENT_WORK_LEASE_TTL \
    STRIPE_MODE STRIPE_SECRET_KEY STRIPE_WEBHOOK_SECRET STRIPE_ACCOUNT_ID STRIPE_CREDENTIAL_REF STRIPE_API_VERSION \
    STRIPE_SUCCESS_URL STRIPE_CANCEL_URL STRIPE_MAX_AMOUNT_JPY APP_PUBLIC_ORIGIN CHECKOUT_PROOF_TTL CHECKOUT_SIGNING_KEYS \
    STAFF_AUTH_MODE STAFF_OIDC_ISSUER STAFF_OIDC_CLIENT_ID STAFF_OIDC_CLIENT_SECRET STAFF_OIDC_DISCOVERY_URL \
    KEYCLOAK_PORT KEYCLOAK_PUBLIC_URL KEYCLOAK_BACKCHANNEL_LOGOUT_URL KC_BOOTSTRAP_ADMIN_USERNAME KC_BOOTSTRAP_ADMIN_PASSWORD TOJIMA_STAFF_CLIENT_SECRET \
    COMPOSE_PROFILES; do unset "$smoke_key"; done
# Reserve two distinct loopback ports while selecting them; Compose binds them
# immediately below (a competing host process can still cause a clean failure).
python3 - <<'PY' >"$smoke_tmp/ports"
import socket
sockets = [socket.socket(), socket.socket()]
for s in sockets:
    s.bind(('127.0.0.1', 0))
    print(s.getsockname()[1])
for s in sockets: s.close()
PY
smoke_app_port=$(sed -n '1p' "$smoke_tmp/ports")
smoke_kc_port=$(sed -n '2p' "$smoke_tmp/ports")
smoke_client_secret=$(openssl rand -hex 48)
umask 077
cat >"$smoke_tmp/.env" <<ENV
POSTGRES_USER=tojima
POSTGRES_DB=tojima
POSTGRES_PASSWORD=$(openssl rand -hex 32)
APP_DB_USER=tojima_app
APP_DB_PASSWORD=$(openssl rand -hex 32)
KEYCLOAK_DB_PASSWORD=$(openssl rand -hex 32)
APP_BIND=127.0.0.1
APP_PORT=$smoke_app_port
KEYCLOAK_PORT=$smoke_kc_port
KEYCLOAK_PUBLIC_URL=http://127.0.0.1:$smoke_kc_port
KEYCLOAK_BACKCHANNEL_LOGOUT_URL=http://server:8080/staff/auth/backchannel-logout
KC_BOOTSTRAP_ADMIN_USERNAME=accept-admin
KC_BOOTSTRAP_ADMIN_PASSWORD=$(openssl rand -hex 32)
TOJIMA_STAFF_CLIENT_SECRET=$smoke_client_secret
STAFF_AUTH_MODE=oidc
STAFF_OIDC_ISSUER=http://127.0.0.1:$smoke_kc_port/realms/tojima
STAFF_OIDC_DISCOVERY_URL=http://keycloak:8080/realms/tojima
STAFF_OIDC_CLIENT_ID=tojima-staff
STAFF_OIDC_CLIENT_SECRET=$smoke_client_secret
APP_PUBLIC_ORIGIN=http://127.0.0.1:$smoke_app_port
SESSION_TTL=4h
GRANT_TTL=1h
GRANT_WINDOW=1m
GRANT_WINDOW_MAX=100
PIN_TTL=15m
PIN_LOCKOUT=1m
PIN_MAX_ATTEMPTS=3
PIN_LENGTH=6
PIN_PEPPER=$(openssl rand -base64 32)
PAYMENT_CREATE_RETRY_WINDOW=20h
PAYMENT_CONTINUATION_TTL=30m
PAYMENT_WORK_LEASE_TTL=30s
STRIPE_MODE=disabled
ENV
smoke_dangling_before=$(docker volume ls -qf dangling=true) || fail 'cannot check dangling volumes'
smoke_docker_checked=true
echo "dangling volumes before: ${smoke_dangling_before:-none}"
echo "project: $smoke_project"
TZ=Asia/Tokyo date '+Started %F %T %Z'
compose up -d --build --wait --wait-timeout 360 || fail 'docker compose up --wait'
# Operations access resolves a real, enabled store before authorization.
compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U tojima -d tojima >/dev/null <<'SQL' || fail 'seed tenant/store'
INSERT INTO tenants(tenant_id,name) VALUES ('t-accept','Staff acceptance');
INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode,guest_display_name)
VALUES ('t-accept','s-accept','Staff acceptance','DIRECT_SCAN','Staff acceptance');
SQL
python3 - "$smoke_root" "$smoke_tmp/.env" "$smoke_project" <<'PY' || fail 'acceptance driver'
"""Only disposable loopback HTTP: intentionally resend Secure cookies manually.

This exercises the real IdP/BFF protocol, not browser cookie enforcement or
Passkey (F2). Never print response bodies, query strings, cookies or exceptions
containing provider data. CONFIGURE_TOTP enrollment uses the manual-entry page.
"""
import base64
import datetime
import hashlib
import hmac
from html.parser import HTMLParser
from http.cookies import SimpleCookie
import json
from pathlib import Path
import re
import secrets
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT, ENV_FILE, PROJECT = sys.argv[1:]
ENV = dict(line.split('=', 1) for line in Path(ENV_FILE).read_text().splitlines() if '=' in line)
APP = ENV['APP_PUBLIC_ORIGIN']
KC = ENV['KEYCLOAK_PUBLIC_URL']
SESSION = '__Host-tojima_staff'
TX = '__Host-tojima_staff_tx'
BROWSER = '__Host-tojima_staff_browser'
API = APP + '/v1/staff/tenants/t-accept/stores/s-accept/operations/access'
STEP = 'setup'
CONTEXT = 'HTTP=none path=none form_action=none'
JST = datetime.timezone(datetime.timedelta(hours=9), 'JST')


class Failure(Exception):
    pass


def check(condition, message):
    if not condition:
        raise Failure(message)


def stamp():
    return datetime.datetime.now(JST).strftime('%Y-%m-%d %H:%M:%S JST')


def passed():
    print('PASS ' + STEP + ' ' + stamp(), flush=True)


def origin(url):
    u = urllib.parse.urlsplit(url)
    return u.scheme + '://' + u.netloc


def path(url):
    # Never include query/fragment: actions and callbacks contain credentials.
    return urllib.parse.urlsplit(url).path


def response_cookies(headers):
    cookies = SimpleCookie()
    for raw in headers.get_all('Set-Cookie', []):
        cookies.load(raw)
    return cookies


def cookie_contract(cookies, name):
    check(name in cookies and bool(cookies[name].value), 'missing cookie ' + name)
    cookie = cookies[name]
    check(bool(cookie['secure']) and bool(cookie['httponly']) and
          cookie['path'] == '/' and not cookie['domain'] and
          cookie['samesite'].lower() == 'lax', 'cookie attributes invalid: ' + name)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class HTTP:
    def __init__(self):
        self.cookies = {}
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        self.action = 'none'

    def request(self, url, method='GET', data=None, headers=None, cookie_override=None):
        global CONTEXT
        check(origin(url) in (APP, KC), 'unexpected destination origin')
        headers = dict(headers or {})
        selected = []
        for (site, name, cookie_path), value in self.cookies.items():
            request_path = path(url)
            if site == origin(url) and (request_path == cookie_path or
                    request_path.startswith(cookie_path.rstrip('/') + '/')):
                selected.append((name, value))
        if cookie_override is not None:
            selected = [(k, v) for k, v in selected if k != SESSION]
            selected.append((SESSION, cookie_override))
        if origin(url) == APP and path(url) == '/staff/auth/callback':
            sent = dict(selected)
            check(bool(sent.get(TX)) and bool(sent.get(BROWSER)), 'callback binding cookies not propagated')
        if selected:
            headers['Cookie'] = '; '.join(k + '=' + v for k, v in selected)
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        CONTEXT = 'HTTP=pending path=' + path(url) + ' form_action=' + self.action
        try:
            response = self.opener.open(req, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            status, response_headers, body = response.code, response.headers, response.read()
        CONTEXT = 'HTTP=' + str(status) + ' path=' + path(url) + ' form_action=' + self.action
        # Revoked-cookie probes are observational: a deletion header for the
        # old token must not erase the active browser's rotated session.
        if cookie_override is not None:
            return status, response_headers, body, url
        for raw in response_headers.get_all('Set-Cookie', []):
            jar = SimpleCookie()
            jar.load(raw)
            for name, morsel in jar.items():
                key = (origin(url), name, morsel['path'] or '/')
                if morsel['max-age'] and int(morsel['max-age']) <= 0:
                    self.cookies.pop(key, None)
                else:
                    self.cookies[key] = morsel.value
        return status, response_headers, body, url

    def follow(self, response):
        for _ in range(20):
            status, headers, body, url = response
            # Inspect the callback's actual status, Location and new cookie.
            if origin(url) == APP and path(url) == '/staff/auth/callback':
                return response
            if status not in (301, 302, 303, 307, 308):
                return response
            location = headers.get('Location')
            check(bool(location), 'redirect missing Location')
            response = self.request(urllib.parse.urljoin(url, location))
        raise Failure('redirect limit exceeded')

    def cookie(self):
        return self.cookies.get((APP, SESSION, '/'))


class Page(HTMLParser):
    def __init__(self, response):
        super().__init__(convert_charrefs=True)
        self.response = response
        self.forms, self.links, self.secret_text = [], {}, []
        self.form = None
        self.in_secret = False
        self.feed(response[2].decode('utf-8'))

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == 'form':
            self.form = {'action': a.get('action', ''), 'method': a.get('method', 'get').upper(), 'inputs': {}}
            self.forms.append(self.form)
        if tag == 'input' and self.form is not None and a.get('name') and 'disabled' not in a:
            kind = a.get('type', 'text').lower()
            if kind in ('button', 'reset', 'submit', 'image'):
                return
            if kind in ('checkbox', 'radio') and 'checked' not in a:
                return
            self.form['inputs'][a['name']] = a.get('value', '')
        if tag == 'a' and a.get('id'):
            self.links[a['id']] = a.get('href', '')
        if a.get('id') == 'kc-totp-secret-key':
            self.in_secret = True

    def handle_endtag(self, tag):
        if tag == 'form':
            self.form = None
        if tag == 'span':
            self.in_secret = False

    def handle_data(self, text):
        if self.in_secret:
            self.secret_text.append(text)

    def find(self, field):
        forms = [f for f in self.forms if field in f['inputs']]
        check(len(forms) == 1, 'expected exactly one form with field ' + field)
        return forms[0]

    def has(self, field):
        return any(field in f['inputs'] for f in self.forms)


def submit(browser, page, field, values):
    form = page.find(field)
    target = urllib.parse.urljoin(page.response[3], form['action'])
    browser.action = path(target)
    check(origin(target) == KC and path(target).startswith('/realms/tojima/login-actions/'),
          'unexpected Keycloak form action')
    check(form['method'] == 'POST', 'expected POST form')
    fields = dict(form['inputs'])
    fields.update(values)
    response = browser.request(target, 'POST', urllib.parse.urlencode(fields).encode(),
                               {'Content-Type': 'application/x-www-form-urlencoded'})
    return browser.follow(response)


ADMIN = HTTP()
admin_token, admin_until = '', 0


def admin(route, method='GET', payload=None, expected=200):
    global admin_token, admin_until
    if time.monotonic() >= admin_until:
        form = urllib.parse.urlencode({
            'client_id': 'admin-cli', 'grant_type': 'password',
            'username': ENV['KC_BOOTSTRAP_ADMIN_USERNAME'],
            'password': ENV['KC_BOOTSTRAP_ADMIN_PASSWORD'],
        }).encode()
        response = ADMIN.request(KC + '/realms/master/protocol/openid-connect/token', 'POST', form,
                                 {'Content-Type': 'application/x-www-form-urlencoded'})
        check(response[0] == 200, 'bootstrap admin token failed')
        token = json.loads(response[2])
        admin_token = token['access_token']
        admin_until = time.monotonic() + max(1, token['expires_in'] - 10)
    data = None if payload is None else json.dumps(payload).encode()
    response = ADMIN.request(KC + '/admin/realms/tojima/' + route, method, data,
                             {'Authorization': 'Bearer ' + admin_token, 'Content-Type': 'application/json'})
    check(response[0] == expected, 'admin REST unexpected status; expected ' + str(expected))
    return json.loads(response[2]) if response[2] else None


def staffadmin(command, *args):
    result = subprocess.run([
        'docker', 'compose', '-f', str(Path(ROOT) / 'compose.yaml'), '--env-file', ENV_FILE,
        '-p', PROJECT, '--profile', 'staff', 'run', '--rm', '--no-deps', '--entrypoint',
        '/staffadmin', 'server', command, '--actor', 'acceptance', '--subject', subject, *args,
    ], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
    # Do not replay subprocess output: future CLI/Compose errors could expose env.
    check(result.returncode == 0, 'staffadmin ' + command + ' exit=' + str(result.returncode))


def begin(browser, purpose='login'):
    previous_browser = browser.cookies.get((APP, BROWSER, '/'))
    response = browser.request(APP + '/staff/auth/' + purpose + '?return=/staff/')
    check(response[0] == 302, 'auth begin expected 302')
    cookies = response_cookies(response[1])
    cookie_contract(cookies, TX)
    check(browser.cookies.get((APP, TX, '/')) == cookies[TX].value, 'transaction cookie not retained')
    if previous_browser is None or BROWSER in cookies:
        cookie_contract(cookies, BROWSER)
        check(browser.cookies.get((APP, BROWSER, '/')) == cookies[BROWSER].value, 'browser cookie not retained')
    else:
        check(browser.cookies.get((APP, BROWSER, '/')) == previous_browser, 'browser binding changed')
    target = urllib.parse.urljoin(response[3], response[1].get('Location', ''))
    check(origin(target) == KC, 'auth redirect must reach Keycloak')
    query = urllib.parse.parse_qs(urllib.parse.urlsplit(target).query)
    check(query.get('prompt') == ['login'], 'auth request missing prompt=login')
    check(query.get('acr_values') == [('strong' if purpose == 'step-up' else 'basic')], 'wrong requested ACR')
    if purpose == 'step-up':
        check(query.get('max_age') == ['0'], 'step-up missing max_age=0')
    return browser.follow(response)


def password(browser, response):
    check(response[0] == 200 and origin(response[3]) == KC, 'password page expected')
    page = Page(response)
    check(page.has('password') and not page.has('otp') and not page.has('totp'), 'password form expected')
    return submit(browser, page, 'password', {'username': 'accept-manager', 'password': user_password})


def callback(response, refused=False):
    status, headers, _, url = response
    check(origin(url) == APP and path(url) == '/staff/auth/callback', 'callback not reached (unexpected form/challenge)')
    check(status == 303, 'callback expected 303')
    check(headers.get('Location') == ('/staff/?auth_error=1' if refused else '/staff/'),
          'callback return mismatch')
    cookies = response_cookies(headers)
    if refused:
        check(SESSION not in cookies or not cookies[SESSION].value, 'denied callback issued session cookie')
    else:
        cookie_contract(cookies, SESSION)


def session(browser, mfa=False, fresh=False, status=200, old=None):
    response = browser.request(APP + '/staff/auth/session', cookie_override=old)
    check(response[0] == status, 'session expected ' + str(status))
    if status == 200:
        data = json.loads(response[2])
        check(data.get('subject_id') == subject, 'session subject mismatch')
        check(data.get('mfa') is mfa and data.get('step_up_fresh') is fresh, 'session MFA/freshness mismatch')


def basic(browser, refused=False):
    response = password(browser, begin(browser))
    # Any OTP/configure form fails here; a basic login must not ask for OTP.
    callback(response, refused)
    if not refused:
        session(browser)


last_otp_window = -1


def otp_code():
    global last_otp_window
    now = time.time()
    window = int(now // 30)
    # Keycloak rejects replay. Also avoid a code about to expire in transit.
    if window <= last_otp_window or now % 30 > 25:
        time.sleep((max(window, last_otp_window) + 1) * 30 - now + 1)
        window = int(time.time() // 30)
    last_otp_window = window
    key = base64.b32decode(otp_secret + '=' * ((-len(otp_secret)) % 8))
    digest = hmac.new(key, struct.pack('>Q', window), hashlib.sha1).digest()
    offset = digest[-1] & 15
    return '%06d' % ((struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000)


def strong(browser):
    response = password(browser, begin(browser, 'step-up'))
    check(response[0] == 200 and origin(response[3]) == KC, 'step-up must show OTP (no silent SSO)')
    page = Page(response)
    check(page.has('otp'), 'step-up OTP form missing')
    callback(submit(browser, page, 'otp', {'otp': otp_code()}))


try:
    # BFF readiness is independent of liveness. Retry only its documented 503.
    browser = HTTP()
    for attempt in range(45):
        ready = browser.request(APP + '/staff/auth/login?return=/staff/')
        if ready[0] != 503:
            check(ready[0] == 302, 'discovery readiness expected 302 or 503')
            break
        time.sleep(1)
    else:
        raise Failure('discovery readiness deadline')

    realm = ADMIN.request(KC + '/realms/tojima/.well-known/openid-configuration')
    check(realm[0] == 200 and 'strong' in json.loads(realm[2]).get('acr_values_supported', []),
          'strong ACR discovery missing')
    policy = admin('')
    check(policy.get('verifyEmail') is True, 'realm verifyEmail must be enabled')
    check(policy.get('otpPolicyType', 'totp') == 'totp' and
          policy.get('otpPolicyAlgorithm', 'HmacSHA1') == 'HmacSHA1' and
          policy.get('otpPolicyDigits', 6) == 6 and policy.get('otpPolicyPeriod', 30) == 30,
          'realm OTP policy must be RFC 6238 SHA-1 / 6 digits / 30 seconds')
    user_password = secrets.token_urlsafe(36)
    admin('users', 'POST', {
        'username': 'accept-manager', 'email': 'accept-manager@example.invalid',
        'firstName': 'Acceptance', 'lastName': 'Manager', 'enabled': True, 'emailVerified': True,
        'requiredActions': ['CONFIGURE_TOTP'],
        'credentials': [{'type': 'password', 'value': user_password, 'temporary': False}],
    }, 201)
    users = admin('users?username=accept-manager&exact=true')
    check(len(users) == 1 and users[0]['username'] == 'accept-manager', 'user lookup failed')
    subject = users[0]['id']
    staffadmin('grant', '--tenant', 't-accept', '--store', 's-accept', '--role', 'STORE_MANAGER')

    # Supported required action rather than an undocumented admin OTP writer.
    response = password(browser, begin(browser))
    check(response[0] == 200, 'CONFIGURE_TOTP page expected')
    page = Page(response)
    check(page.has('totp'), 'CONFIGURE_TOTP form missing')
    if not page.secret_text:
        check(bool(page.links.get('mode-manual')), 'manual OTP enrollment link missing')
        response = browser.follow(browser.request(urllib.parse.urljoin(response[3], page.links['mode-manual'])))
        check(response[0] == 200, 'manual OTP enrollment page expected')
        page = Page(response)
    otp_secret = re.sub(r'\s+', '', ''.join(page.secret_text)).upper()
    check(bool(re.fullmatch('[A-Z2-7]+', otp_secret)), 'manual OTP secret unavailable')
    callback(submit(browser, page, 'totp', {'totp': otp_code(), 'userLabel': 'acceptance', 'saveTOTP': 'Submit'}))
    check(any(c.get('type') == 'otp' for c in admin('users/' + subject + '/credentials')), 'OTP enrollment not persisted')
    logout = browser.request(APP + '/staff/auth/logout', 'POST', b'', {'Origin': APP})
    check(logout[0] == 303, 'setup logout expected 303')
    admin('users/' + subject + '/logout', 'POST', expected=204)
    browser = HTTP()
    print('OTP setup: CONFIGURE_TOTP manual-entry form ' + stamp(), flush=True)

    STEP = 'login-basic'
    basic(browser)
    passed()

    STEP = 'staff-api-basic'
    # ADR-0008 decision 5: STORE_MANAGER centrally requires Actor.MFA.
    check(browser.request(API)[0] == 403, 'basic STORE_MANAGER API expected 403')
    passed()

    STEP = 'step-up'
    old = browser.cookie()
    strong(browser)
    check(browser.cookie() != old, 'step-up did not rotate session cookie')
    session(browser, mfa=True, fresh=True)
    access = browser.request(API)
    check(access[0] == 200 and json.loads(access[2]).get('can_manage') is True,
          'strong STORE_MANAGER API expected 200/can_manage')
    session(browser, status=401, old=old)
    passed()

    STEP = 'step-up-fresh-ceremony'
    # Begin immediately after a successful strong ceremony, inside its 300s TTL.
    # The OTP form must appear before waiting for a new TOTP window.
    old = browser.cookie()
    strong(browser)
    check(browser.cookie() != old, 'second step-up did not rotate session cookie')
    session(browser, mfa=True, fresh=True)
    passed()

    STEP = 'prompt-login'
    # Retain every existing staff-client IdP session ID, including the current
    # login's session. Confirm they survive the authoritative app-only logout.
    sso_before = {s['id'] for s in admin('users/' + subject + '/sessions')
                  if 'tojima-staff' in s.get('clients', {}).values()}
    check(bool(sso_before), 'no live staff IdP SSO session before logout')
    old = browser.cookie()
    logout = browser.request(APP + '/staff/auth/logout', 'POST', b'', {'Origin': APP})
    check(logout[0] == 303 and browser.cookie() is None, 'logout expected 303/cleared session')
    session(browser, status=401, old=old)
    sso_after = {s['id'] for s in admin('users/' + subject + '/sessions')}
    check(sso_before <= sso_after, 'IdP SSO did not survive app-only logout')
    # Deliberately leave the RP logout redirect/confirmation unvisited. The IdP
    # SSO session still exists, so this tests the BFF's prompt=login protection.
    basic(browser)
    passed()

    STEP = 'backchannel'
    old = browser.cookie()
    deadline = time.monotonic() + 10
    admin('users/' + subject + '/logout', 'POST', expected=204)
    while True:
        response = browser.request(APP + '/staff/auth/session', cookie_override=old)
        if response[0] == 401:
            check(time.monotonic() <= deadline, 'back-channel exceeded 10 seconds')
            break
        check(response[0] == 200, 'back-channel polling expected 200 or 401')
        check(time.monotonic() < deadline, 'back-channel session still live after 10 seconds')
        time.sleep(0.2)
    passed()

    STEP = 'hold-restore'
    basic(browser)
    old = browser.cookie()
    staffadmin('hold', '--reason', 'acceptance')
    session(browser, status=401, old=old)
    basic(browser, refused=True)
    denied = browser.cookie()
    session(browser, status=401)
    staffadmin('restore')
    session(browser, status=401, old=old)
    session(browser, status=401, old=denied)
    basic(browser)
    check(browser.cookie() != old, 'restore reused old cookie')
    session(browser, status=401, old=old)
    passed()

    STEP = 'no-offline'
    clients = admin('clients?clientId=tojima-staff')
    check(len(clients) == 1 and clients[0]['clientId'] == 'tojima-staff', 'client lookup failed')
    client_id = clients[0]['id']
    for kind in ('default-client-scopes', 'optional-client-scopes'):
        check(all(s['name'] != 'offline_access' for s in admin('clients/' + client_id + '/' + kind)),
              'offline_access client scope assigned')
    check(admin('users/' + subject + '/offline-sessions/' + client_id) == [], 'offline sessions present')
    passed()
    print('RESULT: PASS ' + stamp(), flush=True)
except Failure as error:
    print('FAIL ' + STEP + ': ' + str(error) + '; ' + CONTEXT + ' ' + stamp(), file=sys.stderr, flush=True)
    sys.exit(1)
except Exception as error:
    # Exception values can include passwords, form bodies or redirect queries.
    print('FAIL ' + STEP + ': ' + type(error).__name__ + '; ' + CONTEXT + ' ' + stamp(), file=sys.stderr, flush=True)
    sys.exit(1)
PY
TZ=Asia/Tokyo date '+Finished %F %T %Z'
