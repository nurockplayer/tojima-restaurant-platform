import { readdir, readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

// Exact library constants and the non-browser checkout URL fallback. Any new
// URL requires review before a demo can expose synthetic staff credentials.
const allowedURLs = new Set([
  'https://react.dev/errors/',
  'http://www.w3.org/1998/Math/MathML',
  'http://www.w3.org/1999/xlink',
  'http://www.w3.org/2000/svg',
  'http://www.w3.org/XML/1998/namespace',
  'http://localhost',
]);

export async function checkRemoteDemoBundle(directory) {
  await readFile(resolve(directory, 'index.html'));
  const files = await readdir(directory, { recursive: true });
  if (!files.some((file) => file.endsWith('.js'))) throw new Error('Missing JavaScript bundle');
  for (const file of files.filter((file) => /\.(?:js|html|css)$/.test(file))) {
    const source = await readFile(resolve(directory, file), 'utf8');
    for (const [url] of source.matchAll(/(?:https?:)?\/\/[^\s"'`<>\\)]+/g)) {
      if (!allowedURLs.has(url)) throw new Error(`Unexpected URL in demo bundle: ${file}`);
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    if (!process.argv[2]) throw new Error('Missing bundle directory');
    await checkRemoteDemoBundle(process.argv[2]);
  } catch (error) {
    console.error(`remote demo: ${error.message}`);
    process.exitCode = 1;
  }
}
