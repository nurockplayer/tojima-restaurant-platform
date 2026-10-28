"""One-time seed of the internal preview (store, menu, owner account).

Usage: seed.py REPO_ROOT ENV_FILE COMPOSE_PROJECT. Helpers are shared with
scripts/cash-pilot-acceptance.sh. 
Provisioning goes through /storeadmin and identity through the real Keycloak
realm, so no product code is bypassed. Never print response bodies, query
strings, cookies or exceptions containing provider data.
"""
import base64
import csv
import datetime
import hashlib
import hmac
from html.parser import HTMLParser
from http.cookies import SimpleCookie
import io
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
TENANT, STORE, TABLE = 'preview-co', 'main', 't01'
TABLES = ['t01', 't02', 't03', 't04']
STAFF = APP + '/v1/staff/tenants/' + TENANT + '/stores/' + STORE
STAFF_SESSION = STAFF + '/tables/' + TABLE + '/sessions/'
ADMIN = APP + '/v1/admin/tenants/' + TENANT
USER_NAME = 'preview-seed'
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
        # Cloudflare's zone security rejects the default Python-urllib agent.
        headers.setdefault('User-Agent', 'tojima-preview-seed/1')
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
            response = self.opener.open(req, timeout=20)
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


KEYCLOAK_ADMIN = HTTP()
admin_token, admin_until = '', 0


def kc_admin(route, method='GET', payload=None, expected=200):
    global admin_token, admin_until
    if time.monotonic() >= admin_until:
        form = urllib.parse.urlencode({
            'client_id': 'admin-cli', 'grant_type': 'password',
            'username': ENV['KC_BOOTSTRAP_ADMIN_USERNAME'],
            'password': ENV['KC_BOOTSTRAP_ADMIN_PASSWORD'],
        }).encode()
        response = KEYCLOAK_ADMIN.request(KC + '/realms/master/protocol/openid-connect/token', 'POST', form,
                                          {'Content-Type': 'application/x-www-form-urlencoded'})
        check(response[0] == 200, 'bootstrap admin token failed')
        token = json.loads(response[2])
        admin_token = token['access_token']
        admin_until = time.monotonic() + max(1, token['expires_in'] - 10)
    data = None if payload is None else json.dumps(payload).encode()
    response = KEYCLOAK_ADMIN.request(KC + '/admin/realms/tojima/' + route, method, data,
                                      {'Authorization': 'Bearer ' + admin_token, 'Content-Type': 'application/json'})
    check(response[0] == expected, 'admin REST unexpected status; expected ' + str(expected))
    return json.loads(response[2]) if response[2] else None


def container_binary(binary, *args, stdin=None):
    """Run one operator CLI inside the compose project's server service."""
    return subprocess.run([
        'docker', 'compose', '-f', str(Path(ROOT) / 'compose.yaml'), '--env-file', ENV_FILE,
        '-p', PROJECT, '--profile', 'staff', 'run', '--rm', '--no-deps', '-T',
        '--entrypoint', binary, 'server', *args,
    ], input=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180)


def storeadmin(*args, stdin=None):
    result = container_binary('/storeadmin', *args, stdin=stdin)
    # Do not replay subprocess output: future CLI/Compose errors could expose env.
    check(result.returncode == 0, 'storeadmin ' + args[0] + ' exit=' + str(result.returncode))
    return result.stdout.decode('utf-8', 'replace')


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
    return submit(browser, page, 'password', {'username': USER_NAME, 'password': user_password})


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


def session_state(browser, mfa=False, fresh=False, status=200):
    response = browser.request(APP + '/staff/auth/session')
    check(response[0] == status, 'session expected ' + str(status))
    if status == 200:
        data = json.loads(response[2])
        check(data.get('subject_id') == subject, 'session subject mismatch')
        check(data.get('mfa') is mfa and data.get('step_up_fresh') is fresh,
              'session MFA/freshness mismatch')


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


def basic(browser):
    # A basic login asks for the password only; ENROLLED TOTP is requested by
    # the step-up ceremony (acr strong) below, not by an ordinary login.
    callback(password(browser, begin(browser)))


def strong(browser):
    response = password(browser, begin(browser, 'step-up'))
    check(response[0] == 200 and origin(response[3]) == KC, 'step-up must show OTP (no silent SSO)')
    page = Page(response)
    check(page.has('otp'), 'step-up OTP form missing')
    callback(submit(browser, page, 'otp', {'otp': otp_code()}))


STAFF_HTTP = HTTP()
GUEST_HTTP = HTTP()


def call(client, url, method='GET', payload=None, expected=200, token=None, key=None):
    headers = {}
    data = None
    if payload is not None:
        data = json.dumps(payload).encode()
        headers['Content-Type'] = 'application/json'
    if method != 'GET':
        # Every non-GET staff/admin request must carry this exact origin.
        headers['Origin'] = APP
    if token is not None:
        headers['Authorization'] = 'Bearer ' + token
    if key is not None:
        headers['Idempotency-Key'] = key
    status, _, body, _ = client.request(url, method, data, headers)
    check(status == expected, 'unexpected status ' + str(status) + ' for ' + method + ' ' + path(url))
    if not body:
        return None
    try:
        return json.loads(body)
    except ValueError:
        # An unmounted route answers with the mux's plain-text 404.
        check(expected != 200, 'non-JSON response for ' + path(url))
        return None


def idempotent(prefix):
    return prefix + '-' + secrets.token_hex(8)


try:
    # BFF readiness is independent of liveness. Retry only its documented 503.
    for attempt in range(60):
        ready = STAFF_HTTP.request(APP + '/staff/auth/login?return=/staff/')
        if ready[0] != 503:
            check(ready[0] == 302, 'discovery readiness expected 302 or 503')
            break
        time.sleep(1)
    else:
        raise Failure('discovery readiness deadline')

    realm = KEYCLOAK_ADMIN.request(KC + '/realms/tojima/.well-known/openid-configuration')
    check(realm[0] == 200 and 'strong' in json.loads(realm[2]).get('acr_values_supported', []),
          'strong ACR discovery missing')
    policy = kc_admin('')
    check(policy.get('verifyEmail') is True, 'realm verifyEmail must be enabled')
    check(policy.get('otpPolicyType', 'totp') == 'totp' and
          policy.get('otpPolicyAlgorithm', 'HmacSHA1') == 'HmacSHA1' and
          policy.get('otpPolicyDigits', 6) == 6 and policy.get('otpPolicyPeriod', 30) == 30,
          'realm OTP policy must be RFC 6238 SHA-1 / 6 digits / 30 seconds')

    STEP = 'provision-store'
    # /storeadmin converges the tenant, store, tables and cash setting; no SQL.
    spec = json.dumps({
        'tenant': {'id': TENANT, 'name': 'Tojima Preview'},
        'store': {
            'id': STORE, 'name': '試営業プレビュー店', 'guest_display_name': 'プレビュー店',
            'qr_entry_mode': 'STAFF_OPEN', 'cash': {'enabled': True, 'mode': 'PAY_LATER'},
        },
        'tables': [{'id': t, 'name': str(i + 1) + '番卓'} for i, t in enumerate(TABLES)],
    }).encode()
    output = storeadmin('apply', '-f', '/dev/stdin', '-public-origin', APP,
                        '-actor', 'preview-seed', stdin=spec)
    # A table name may contain spaces; the locator is the third field from the
    # end of "table <id> <name...> <locator> <url> <status>".
    locators = {}
    for line in output.splitlines():
        fields = line.split()
        if len(fields) >= 6 and fields[0] == 'table':
            locators[fields[1]] = (fields[-3], fields[-2])
    check(set(locators) == set(TABLES), 'storeadmin apply did not report every table')
    locator, locator_url = locators[TABLE]
    check(locator_url == APP + '/?table=' + locator, 'apply rendered an unexpected table URL')
    passed()

    STEP = 'staff-identity'
    user_password = secrets.token_urlsafe(36)
    kc_admin('users', 'POST', {
        'username': USER_NAME, 'email': USER_NAME + '@example.invalid',
        'firstName': 'Preview', 'lastName': 'Seed', 'enabled': True, 'emailVerified': True,
        'requiredActions': ['CONFIGURE_TOTP'],
        'credentials': [{'type': 'password', 'value': user_password, 'temporary': False}],
    }, 201)
    users = kc_admin('users?username=' + USER_NAME + '&exact=true')
    check(len(users) == 1 and users[0]['username'] == USER_NAME, 'user lookup failed')
    subject = users[0]['id']

    def staffadmin(command, *args):
        result = container_binary('/staffadmin', command, '--actor', 'preview-seed', '--subject', subject, *args)
        check(result.returncode == 0, 'staffadmin ' + command + ' exit=' + str(result.returncode))

    # The company menu needs TENANT_ADMIN plus MFA; KDS work needs KITCHEN;
    # table open/close and cash need STORE_MANAGER or STAFF_CASHIER.
    staffadmin('grant', '--tenant', TENANT, '--role', 'TENANT_ADMIN')
    staffadmin('grant', '--tenant', TENANT, '--store', STORE, '--role', 'STORE_MANAGER')
    staffadmin('grant', '--tenant', TENANT, '--store', STORE, '--role', 'KITCHEN')
    staffadmin('grant', '--tenant', TENANT, '--store', STORE, '--role', 'STAFF_CASHIER')

    # Supported required action rather than an undocumented admin OTP writer.
    response = password(STAFF_HTTP, begin(STAFF_HTTP))
    check(response[0] == 200, 'CONFIGURE_TOTP page expected')
    page = Page(response)
    check(page.has('totp'), 'CONFIGURE_TOTP form missing')
    if not page.secret_text:
        check(bool(page.links.get('mode-manual')), 'manual OTP enrollment link missing')
        response = STAFF_HTTP.follow(STAFF_HTTP.request(urllib.parse.urljoin(response[3], page.links['mode-manual'])))
        check(response[0] == 200, 'manual OTP enrollment page expected')
        page = Page(response)
    otp_secret = re.sub(r'\s+', '', ''.join(page.secret_text)).upper()
    check(bool(re.fullmatch('[A-Z2-7]+', otp_secret)), 'manual OTP secret unavailable')
    callback(submit(STAFF_HTTP, page, 'totp',
                    {'totp': otp_code(), 'userLabel': 'preview-seed', 'saveTOTP': 'Submit'}))
    check(any(c.get('type') == 'otp' for c in kc_admin('users/' + subject + '/credentials')),
          'OTP enrollment not persisted')
    logout = STAFF_HTTP.request(APP + '/staff/auth/logout', 'POST', b'', {'Origin': APP})
    check(logout[0] == 303, 'setup logout expected 303')
    kc_admin('users/' + subject + '/logout', 'POST', expected=204)

    # Step-up needs a live staff session, so sign in first, then promote to the
    # MFA actor every admin/staff call requires.
    basic(STAFF_HTTP)
    strong(STAFF_HTTP)
    session_state(STAFF_HTTP, mfa=True, fresh=True)
    passed()

    STEP = 'menu-publish'
    MENU = [
        ('meals', 'お食事', [('teishoku', '日替わり定食', 1100), ('ramen', '醤油ラーメン', 950), ('curry', 'カレーライス', 900)]),
        ('drinks', 'お飲み物', [('beer', '生ビール', 600), ('oolong', 'ウーロン茶', 300)]),
    ]
    draft = call(STAFF_HTTP, ADMIN + '/menu/draft', 'PUT', {
        'version': 0,
        'categories': [{
            'key': ck, 'name': cn, 'position': ci,
            'products': [{'key': pk, 'name': pn, 'description': '', 'base_price_minor': price,
                          'position': pi, 'active': True, 'option_groups': []}
                         for pi, (pk, pn, price) in enumerate(items)],
        } for ci, (ck, cn, items) in enumerate(MENU)],
    })
    draft_version = draft.get('version')
    check(isinstance(draft_version, int) and draft_version >= 1, 'menu draft version missing')
    preview = call(STAFF_HTTP, ADMIN + '/menu/preview', 'POST', {'expected_version': draft_version})
    check(preview.get('can_publish') is True, 'company menu preview is not publishable')
    call(STAFF_HTTP, ADMIN + '/menu/publish', 'POST', {
        'expected_version': draft_version, 'preview_fingerprint': preview['preview_fingerprint'],
    })
    menu = call(STAFF_HTTP, ADMIN + '/stores/' + STORE + '/menu')
    products = [p for c in menu.get('categories', []) for p in c.get('products', [])]
    check(sorted(p.get('key') for p in products) == sorted(pk for _, _, items in MENU for pk, _, _ in items),
          'store menu does not carry exactly the published products')
    for product in products:
        available = call(STAFF_HTTP, ADMIN + '/stores/' + STORE + '/menu/availability', 'PUT', {
            'target': {'kind': 'PRODUCT', 'product_key': product['key']},
            'status': 'AVAILABLE',
            'expected_version': product.get('availability_version', 0),
        })
        check(available.get('status') == 'AVAILABLE', 'availability not AVAILABLE: ' + product['key'])
    passed()

    STEP = 'owner-account'
    # The Owner sets a new password and enrolls OTP on first sign-in.
    owner_password = secrets.token_urlsafe(12)
    kc_admin('users', 'POST', {
        'username': 'owner', 'email': 'owner@example.invalid',
        'firstName': 'Owner', 'lastName': 'Preview', 'enabled': True, 'emailVerified': True,
        'requiredActions': ['UPDATE_PASSWORD', 'CONFIGURE_TOTP'],
        'credentials': [{'type': 'password', 'value': owner_password, 'temporary': True}],
    }, 201)
    owner = kc_admin('users?username=owner&exact=true')
    check(len(owner) == 1, 'owner lookup failed')
    for role_args in (('--role', 'TENANT_ADMIN'), ('--store', STORE, '--role', 'STORE_MANAGER'),
                      ('--store', STORE, '--role', 'KITCHEN'), ('--store', STORE, '--role', 'STAFF_CASHIER')):
        result = container_binary('/staffadmin', 'grant', '--actor', 'preview-seed', '--subject', owner[0]['id'],
                                  '--tenant', TENANT, *role_args)
        check(result.returncode == 0, 'owner grant exit=' + str(result.returncode))
    passed()

    STEP = 'retire-seed'
    STAFF_HTTP.request(APP + '/staff/auth/logout', 'POST', b'', {'Origin': APP})
    result = container_binary('/staffadmin', 'hold', '--actor', 'preview-seed', '--subject', subject,
                              '--reason', 'preview seed finished')
    check(result.returncode == 0, 'seed hold exit=' + str(result.returncode))
    kc_admin('users/' + subject, 'DELETE', expected=204)
    passed()

    STEP = 'outputs'
    out = Path(ENV_FILE).parent
    listing = storeadmin('tables', '-tenant', TENANT, '-store', STORE, '-public-origin', APP, '-csv')
    (out / 'tables.csv').write_text(listing)
    (out / 'owner-credentials.txt').write_text(
        'Staff URL: ' + APP + '/staff/\nUsername: owner\nTemporary password: ' + owner_password + '\n')
    (out / 'owner-credentials.txt').chmod(0o600)
    passed()
    print('RESULT: PASS ' + stamp(), flush=True)
except Failure as error:
    print('FAIL ' + STEP + ': ' + str(error) + '; ' + CONTEXT + ' ' + stamp(), file=sys.stderr, flush=True)
    sys.exit(1)
except Exception as error:
    print('FAIL ' + STEP + ': ' + type(error).__name__ + '; ' + CONTEXT + ' ' + stamp(), file=sys.stderr, flush=True)
    sys.exit(1)
