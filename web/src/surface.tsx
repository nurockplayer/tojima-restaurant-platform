import { Component, lazy, Suspense } from 'react';
import type { ComponentType, ReactNode } from 'react';
import type { DemoSurfaceProps } from './ui/DemoBanner';

export type SurfaceName = 'guest' | 'staff' | 'catalog' | 'order';

export function demoEnabled(root: HTMLElement | null): boolean {
  return root?.dataset.demo === 'shironagasukujira';
}

/** Staff and catalog consoles use explicit routes; every other path keeps the guest flow and its query string. */
export function surfaceFor(pathname: string): SurfaceName {
  const path = pathname.replace(/\/$/, '');
  return path === '/order' ? 'order' : path === '/staff' || path === '/kitchen' ? 'staff' : path === '/catalog' ? 'catalog' : 'guest';
}

export function initialStaffViewFor(pathname: string): 'tables' | 'kds' {
  return pathname.replace(/\/$/, '') === '/kitchen' ? 'kds' : 'tables';
}

// Each surface is its own chunk, so guest phones never download the staff/KDS or catalog consoles.
const surfaces: Record<SurfaceName, ComponentType<DemoSurfaceProps>> = {
  order: lazy(() => import('./ui/OrderRead').then((module) => ({ default: module.OrderPage }))),
  guest: lazy(() => import('./ui/App').then((module) => ({ default: module.App }))),
  staff: lazy(() => import('./ui/StaffApp').then((module) => ({ default: module.StaffApp }))),
  catalog: lazy(() => import('./ui/CatalogApp').then((module) => ({ default: module.CatalogApp }))),
};

const copy = {
  loading: '読み込み中… / 載入中…',
  failed: '画面を読み込めませんでした。通信状態を確認して再読み込みしてください。 / 無法載入畫面，請確認網路後重新載入。',
  reload: '再読み込み / 重新載入',
};

interface BoundaryProps { children: ReactNode; onReload: () => void }

/** A failed chunk download (offline, or stale assets after a deploy) shows a reload action instead of a blank page. */
export class SurfaceBoundary extends Component<BoundaryProps, { failed: boolean }> {
  override state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  override render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="loading-card" role="alert">
        <p>{copy.failed}</p>
        <button type="button" className="button primary small" onClick={this.props.onReload}>{copy.reload}</button>
      </div>
    );
  }
}

export function Surface({ name, demo = false, initialView = 'tables', staffAuth = { kind: 'unsupported' }, onReload = () => window.location.reload() }: DemoSurfaceProps & { name: SurfaceName; onReload?: () => void }) {
  const Selected = surfaces[name];
  return (
    <SurfaceBoundary onReload={onReload}>
      <Suspense fallback={<div className="loading-card" role="status">{copy.loading}</div>}>
        <Selected demo={demo} initialView={initialView} staffAuth={staffAuth} />
      </Suspense>
    </SurfaceBoundary>
  );
}
