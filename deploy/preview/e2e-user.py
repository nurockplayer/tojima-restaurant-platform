"""Create the preview's E2E staff user (e2e-bot) once.

usage: python3 deploy/preview/e2e-user.py <repo root> <.env> <compose project>

The user gets a fixed password and the same roles as the owner. Keycloak asks
it to enroll OTP on its first sign-in; e2e/journey.mjs does that and stores the
secret next to the password in e2e-credentials.json (0600, never committed).
"""
import json
import os
import secrets
import subprocess
import sys
import urllib.parse
import urllib.request
from pathlib import Path

ROOT, ENV_FILE, PROJECT = sys.argv[1:]
os.umask(0o077)
OUT = Path(ENV_FILE).parent / 'e2e-credentials.json'
if OUT.exists():
    sys.exit('e2e user already created: ' + str(OUT))
ENV = dict(line.split('=', 1) for line in Path(ENV_FILE).read_text().splitlines() if '=' in line)
KC = ENV['KEYCLOAK_PUBLIC_URL']
TENANT, STORE, USERNAME = 'preview-co', 'main', 'e2e-bot'
# Cloudflare's zone security refuses the default Python-urllib User-Agent.
UA = {'User-Agent': 'tojima-preview-seed/1'}


def request(url, method='GET', data=None, headers=None):
    req = urllib.request.Request(url, data, {**UA, **(headers or {})}, method=method)
    with urllib.request.urlopen(req, timeout=30) as response:
        return response.status, response.read()


status, body = request(KC + '/realms/master/protocol/openid-connect/token', 'POST', urllib.parse.urlencode({
    'client_id': 'admin-cli', 'grant_type': 'password',
    'username': ENV['KC_BOOTSTRAP_ADMIN_USERNAME'], 'password': ENV['KC_BOOTSTRAP_ADMIN_PASSWORD'],
}).encode(), {'Content-Type': 'application/x-www-form-urlencoded'})
token = json.loads(body)['access_token']
admin = {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'}

# A run that failed after creating the user left no credentials file: start over.
for stale in json.loads(request(KC + '/admin/realms/tojima/users?exact=true&username=' + USERNAME, headers=admin)[1]):
    request(KC + '/admin/realms/tojima/users/' + stale['id'], 'DELETE', headers=admin)

password = secrets.token_urlsafe(18)
request(KC + '/admin/realms/tojima/users', 'POST', json.dumps({
    'username': USERNAME, 'email': 'e2e-bot@example.invalid',
    'firstName': 'E2E', 'lastName': 'Bot', 'enabled': True, 'emailVerified': True,
    'requiredActions': ['CONFIGURE_TOTP'],
    'credentials': [{'type': 'password', 'value': password, 'temporary': False}],
}).encode(), admin)
users = json.loads(request(KC + '/admin/realms/tojima/users?exact=true&username=' + USERNAME, headers=admin)[1])
if len(users) != 1:
    sys.exit('e2e user lookup failed')
subject = users[0]['id']

for role_args in (('--role', 'TENANT_ADMIN'), ('--store', STORE, '--role', 'STORE_MANAGER'),
                  ('--store', STORE, '--role', 'KITCHEN'), ('--store', STORE, '--role', 'STAFF_CASHIER')):
    result = subprocess.run([
        'docker', 'compose', '-f', str(Path(ROOT) / 'compose.yaml'), '--env-file', ENV_FILE,
        '-p', PROJECT, '--profile', 'staff', 'run', '--rm', '--no-deps', '-T',
        '--entrypoint', '/staffadmin', 'server',
        'grant', '--actor', 'e2e-user.py', '--subject', subject, '--tenant', TENANT, *role_args,
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=180)
    if result.returncode != 0:
        sys.exit('grant failed: ' + ' '.join(role_args) + ' exit=' + str(result.returncode))

OUT.write_text(json.dumps({'username': USERNAME, 'password': password, 'totpSecret': None}, indent=2) + '\n')
print('created ' + USERNAME + '; credentials in ' + str(OUT))
