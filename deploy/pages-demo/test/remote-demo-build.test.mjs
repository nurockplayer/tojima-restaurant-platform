import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { checkRemoteDemoBundle } from '../../../scripts/check-remote-demo-bundle.mjs';

const repo = fileURLToPath(new URL('../../../', import.meta.url));

test('demo build ignores inherited and dotenv API overrides, including bearer requests', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'tojima-demo-build-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const file of ['.env', '.env.local', '.env.remote-demo', '.env.remote-demo.local']) {
    await writeFile(join(root, file), 'VITE_API_BASE_URL=https://dotenv-collector.invalid\nVITE_OTHER_API_BASE_URL=https://dotenv-other.invalid\n');
  }
  await writeFile(join(root, 'entry.ts'), `
    export { apiBase, request, bearerHeaders } from ${JSON.stringify(join(repo, 'web/src/shared/http.ts'))};
    export const otherBase = import.meta.env.VITE_OTHER_API_BASE_URL;
  `);
  const built = spawnSync(process.execPath, ['--input-type=module', '--eval', `
    import { build } from ${JSON.stringify(new URL('../../../web/node_modules/vite/dist/node/index.js', import.meta.url).href)};
    await build({
      root: ${JSON.stringify(root)},
      configFile: ${JSON.stringify(join(repo, 'web/vite.config.ts'))},
      mode: 'remote-demo',
      build: { lib: { entry: ${JSON.stringify(join(root, 'entry.ts'))}, formats: ['es'], fileName: () => 'probe.mjs' } },
    });
  `], {
    encoding: 'utf8',
    env: { ...process.env, VITE_API_BASE_URL: 'https://shell-collector.invalid', VITE_OTHER_API_BASE_URL: 'https://shell-other.invalid' },
  });
  assert.equal(built.status, 0, built.stdout + built.stderr);
  const source = await readFile(join(root, 'dist/probe.mjs'), 'utf8');
  assert.doesNotMatch(source, /collector\.invalid|other\.invalid/);
  const api = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
  assert.equal(api.apiBase(), '');
  assert.equal(api.otherBase, undefined);
  const calls = [];
  t.mock.method(globalThis, 'fetch', async (url, init) => {
    calls.push({ url, init });
    return new Response('{}', { headers: { 'content-type': 'application/json' } });
  });
  await api.request('/v1/staff/operations/access', { headers: api.bearerHeaders('synthetic-test-token') });
  assert.equal(calls[0].url, '/v1/staff/operations/access');
  assert.equal(calls[0].init.headers.authorization, 'Bearer synthetic-test-token');
});

test('bundle check rejects unexpected absolute and protocol-relative URLs and missing output', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'tojima-demo-bundle-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await assert.rejects(checkRemoteDemoBundle(root));
  await writeFile(join(root, 'index.html'), '<div id="root"></div>');
  await assert.rejects(checkRemoteDemoBundle(root), /Missing JavaScript/);
  await mkdir(join(root, 'assets'));
  const bundle = join(root, 'assets/app.js');
  await writeFile(bundle, 'fetch("/v1/staff/operations/access"); const docs="https://react.dev/errors/";');
  await checkRemoteDemoBundle(root);
  for (const url of ['https://collector.invalid', 'http://collector.invalid', '//collector.invalid', 'https://react.dev/errors/collector']) {
    await writeFile(bundle, `fetch(${JSON.stringify(url)});`);
    await assert.rejects(checkRemoteDemoBundle(root), /Unexpected URL/);
  }
});
