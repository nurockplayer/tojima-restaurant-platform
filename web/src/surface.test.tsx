// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SurfaceBoundary, surfaceFor } from './surface';

let root: Root | undefined;
let host: HTMLDivElement | undefined;

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined;
  host?.remove();
  host = undefined;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('surface selection', () => {
  it('opens the staff console only on /staff with or without a trailing slash', () => {
    expect(surfaceFor('/staff')).toBe('staff');
    expect(surfaceFor('/staff/')).toBe('staff');
    expect(surfaceFor('/')).toBe('guest');
    expect(surfaceFor('/staff/extra')).toBe('guest');
    expect(surfaceFor('/staffing')).toBe('guest');
  });

  it('routes /catalog and /catalog/ to the catalog surface', () => {
    expect(surfaceFor('/catalog')).toBe('catalog');
    expect(surfaceFor('/catalog/')).toBe('catalog');
    expect(surfaceFor('/catalog/items')).toBe('guest');
  });
});

describe('surface boundary', () => {
  it('replaces a failed surface load with a reload action', async () => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const onReload = vi.fn<() => void>();
    const Broken = () => { throw new Error('Failed to fetch dynamically imported module'); };
    host = document.createElement('div');
    document.body.append(host);
    root = createRoot(host);

    await act(async () => root?.render(<SurfaceBoundary onReload={onReload}><Broken /></SurfaceBoundary>));

    expect(host.querySelector('[role="alert"]')?.textContent).toContain('再読み込み');
    await act(async () => host?.querySelector('button')?.click());
    expect(onReload).toHaveBeenCalledOnce();
  });
});
