import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useReducer, useRef, useState } from 'react';
import { guestDisplayKind, guestRefundKind, paidUnderReview } from './guest-display';
import { ApiError, amountJPY, cartClient, newOperationKey } from '../api/client';
import type { Cart, GuestGrant, GuestMenu, OptionSelection, Order, PublicTable } from '../api/types';
import { operationsClient } from '../api/operations';
import type { GuestOperations, GuestOrderStatus } from '../api/operations';
import { checkoutClient, checkoutNavigation } from '../api/checkout';
import type { CheckoutState, CheckoutStatus } from '../api/checkout';
import { createReplayOperation, guestCartReducer, initialGuestCartState } from './cart-state';
import { restoreGuestFocus } from './guest/restore-focus';
import { CategoryNav } from './guest/CategoryNav';
import { MenuList } from './guest/MenuList';
import { OptionSheet } from './guest/OptionSheet';
import { CartBar } from './guest/CartBar';
import { CartSheet } from './guest/CartSheet';
import { initialSyncState, syncReducer } from './guest/sync-state';
import type { SyncOrigin } from './guest/sync-state';
import { OrderTracks } from './guest/OrderTracks';
import guestStyles from './guest/GuestApp.module.css';
import { DemoBanner, DemoNotice, demoStoreName } from './DemoBanner';
import type { DemoSurfaceProps } from './DemoBanner';

import { MyOrders, RequestOrderLink } from './OrderRead';
import { rememberOrderGrant } from './my-orders';

type Locale = 'ja' | 'zh';
const MENU_REFRESH_TTL_MS = 15_000;
let guestSurfaceMounts = 0;
const ja = {
    backToMenu: 'メニューに戻る',
    synced: 'テーブルで共有中', syncWaiting: '同期待ち', syncAt: '時点', syncUnstable: '通信が不安定です',
    syncReason: '表示は {time} 時点の内容です。再接続まで注文の確定はできません。カートは保持されます。',
    table: 'テーブル', committed: '注文履歴',
    join: 'ご注文を始める', joinHint: 'テーブルのご案内に従って参加してください。', pin: 'テーブル PIN', enter: '参加する',
    loading: '確認しています…', closed: 'このテーブルは現在ご利用いただけません。スタッフにお声がけください。',
    menu: 'お品書き',
    remove: '削除', latest: '共有カートを確認しました', tableActive: 'こちらのテーブルで注文中', directMode: 'テーブルの QR から参加', pinMode: 'テーブル PIN で参加', staffMode: 'スタッフが開始したテーブル',
    refresh: '共有内容を更新', preview: '金額を確認する', previewHint: '注文を確定する前に最新の金額と内容をご確認ください。',
    total: '税込合計', tax: '内消費税', method: '今回のお支払い方法', stripe: 'クレジットカード', cash: '店頭で現金',
    confirm: 'この内容で注文を確定', working: '送信中…', cancelPreview: 'カートへ戻る',
    order: '確定した注文', orderId: '注文番号', stripeNote: '注文を確定しても、お支払いは完了しません。', cashNote: '現金のお支払いはスタッフが確認します。',
    staffWaiting: 'スタッフの確認待ち', acceptedOrder: '注文を受け付けました',
    already: 'この世代の注文はすでに確定しています。確定した内容をご確認ください。', stale: 'カートまたは価格が更新されました。最新内容で金額を確認し直してください。',
    checkoutStatus: 'カードのお支払い', checkCheckout: '支払い状況を確認', checkoutLoading: '状況を確認しています…', continueCheckout: '支払いを続ける', checkoutSupport: 'この端末でお支払いを続けられない場合は、スタッフにお声がけください。', checkoutError: '支払い状況を確認できません。スタッフにお声がけください。',
    checkoutUnavailable: 'カードのお支払いを準備できませんでした。注文は送信されていません。もう一度お試しください。', resumeError: '支払い画面を開けませんでした。状況を確認してから、スタッフにお声がけください。',
    stateCreatePending: '注文を確認しています', stateCreating: 'お支払い画面を準備しています', stateOpen: 'お支払い手続きができます', stateProcessing: 'お支払いを確認しています', stateUnknown: 'お支払い状況を確認しています', statePaid: 'お支払い済み', stateExpired: 'お支払い期限が切れました', stateFailed: 'お支払いを完了できませんでした', stateReview: 'お支払い状況を店舗で確認しています',
    network: '通信結果を確認できません。再試行すると同じ操作を照合します。', retry: '前回の操作を照合して再試行',
    error: '処理できませんでした。内容を確認してもう一度お試しください。', unavailableLines: '注文できない商品があります。削除または内容を変更してから確認してください。',
    operationsStale: '最新の状況を確認できません。表示は前回確認時の内容です。', amountUnavailable: '最新の注文内容と金額を確認できません。', changed: '店舗により注文内容が変更されました', cancelled: 'この注文は取り消されました', originalTotal: '注文確定時の税込合計', refundPending: '返金処理中', refundFailed: '返金を確認中です。店舗にお問い合わせください', refunded: '返金済み', refundedTotal: '返金合計',
    noOrders: '確定済みの注文はありません。', direct: 'QRテーブルを確認', session: 'セッションから再参加', locale: '繁中', demoCash: '現金（デモ・テスト専用）',
};
const copy: Record<Locale, Record<keyof typeof ja, string>> = { ja, zh: {
    backToMenu: '返回菜單',
    synced: '同桌共用中', syncWaiting: '等待同步', syncAt: '的內容', syncUnstable: '網路連線不穩',
    syncReason: '目前顯示 {time} 的內容。恢復連線前無法送出訂單，購物車內容會保留。',
    table: '桌次', committed: '已送出訂單',
    join: '開始點餐', joinHint: '請依桌邊指示加入本桌。', pin: '桌次 PIN', enter: '加入桌次',
    loading: '正在確認…', closed: '目前無法使用此桌，請洽服務人員。',
    menu: '菜單',
    remove: '移除', latest: '已確認共用購物車', tableActive: '正在此桌點餐', directMode: '透過桌邊 QR 加入', pinMode: '透過桌次 PIN 加入', staffMode: '由服務人員開啟桌次',
    refresh: '更新共用內容', preview: '確認金額', previewHint: '送出前請確認最新品項和金額。',
    total: '含稅總額', tax: '其中消費稅', method: '本次付款方式', stripe: '信用卡', cash: '店內現金',
    confirm: '確認並送出訂單', working: '送出中…', cancelPreview: '返回購物車',
    order: '已成立訂單', orderId: '訂單編號', stripeNote: '確認訂單後，付款仍未完成。', cashNote: '現金付款由服務人員確認收款。',
    staffWaiting: '等待店員確認', acceptedOrder: '訂單已受理',
    already: '此購物車版本已成立訂單，請查看已確認內容。', stale: '購物車或價格已更新，請重新確認最新內容與金額。',
    checkoutStatus: '信用卡付款', checkCheckout: '確認付款狀態', checkoutLoading: '正在確認狀態…', continueCheckout: '繼續付款', checkoutSupport: '若無法在此裝置繼續付款，請洽服務人員。', checkoutError: '目前無法確認付款狀態，請洽服務人員。',
    checkoutUnavailable: '無法準備信用卡付款，訂單尚未送出。請再試一次。', resumeError: '無法開啟付款畫面，請先確認狀態或洽服務人員。',
    stateCreatePending: '正在確認訂單', stateCreating: '正在準備付款畫面', stateOpen: '可以繼續付款', stateProcessing: '正在確認付款', stateUnknown: '正在確認付款狀態', statePaid: '已付款', stateExpired: '付款期限已過', stateFailed: '付款未完成', stateReview: '門店正在確認付款狀態',
    network: '目前無法確認連線結果。重試會以相同操作編號查核，不會重複新增。', retry: '查核並重試上一項操作',
    error: '暫時無法處理，請確認內容後重試。', unavailableLines: '有無法點餐的品項，請移除或調整後再確認。',
    operationsStale: '無法確認最新狀態，目前顯示上次確認的內容。', amountUnavailable: '目前無法確認最新訂單內容與金額。', changed: '店家已調整您的訂單', cancelled: '此訂單已取消', originalTotal: '送單時含稅總額', refundPending: '退款處理中', refundFailed: '退款確認中，請洽店家', refunded: '已退款', refundedTotal: '退款總額',
    noOrders: '目前沒有已確認訂單。', direct: '確認 QR 桌次', session: '使用桌次重新加入', locale: '日本語', demoCash: '現金（展示測試專用）',
  },
};

interface Entry {
  locator?: string;
  sessionID?: string;
  grant: GuestGrant;
  menu: GuestMenu;
}
interface Replay { origin: SyncOrigin; key: string; run: () => Promise<Cart | import('../api/types').CommitResult>; }

function entryFromURL(): { locator?: string; sessionID?: string } {
  const query = new URLSearchParams(window.location.search);
  const locator = query.get('table')?.trim();
  const sessionID = query.get('session')?.trim();
  return { ...(locator ? { locator } : {}), ...(sessionID ? { sessionID } : {}) };
}

export function displayTableLocator(locator: string, demo: boolean): string {
  return demo && locator === 'demo-shironagasukujira-table-05' ? '05' : locator;
}

function syncOrigin(current: Entry): SyncOrigin {
  return { sessionID: current.grant.session.id, token: current.grant.table_join_token };
}
function isOrigin(current: Entry | null, origin: SyncOrigin): boolean {
  return current?.grant.session.id === origin.sessionID && current.grant.table_join_token === origin.token;
}
function storageKey(sessionID: string) { return `tojima.table-join.${sessionID}`; }
function safeAmount(amount: string | number | undefined): string {
  if (typeof amount === 'number' && !Number.isSafeInteger(amount)) return '—';
  return amountJPY(amount);
}
function isSafeAmount(amount: string | number | undefined): boolean {
  if (typeof amount === 'number') return Number.isSafeInteger(amount);
  return typeof amount === 'string' && /^-?\d+$/.test(amount);
}
function isPositiveAmount(amount: string | undefined): boolean {
  return typeof amount === 'string' && /^\d+$/.test(amount) && BigInt(amount) > 0n;
}

export function isCurrentMenuRead(
  sequence: number,
  currentSequence: number,
  requestSessionID: string,
  currentSessionID: string | undefined,
  requestToken: string,
  currentToken: string | undefined,
): boolean {
  return sequence === currentSequence && requestSessionID === currentSessionID && requestToken === currentToken;
}

function guestOrderLabel(order: GuestOrderStatus | undefined, locale: Locale): string {
  const display = order ? guestDisplayKind(order) : 'unknown';
  if (display === 'cancelled') return locale === 'ja' ? 'この注文は取り消されました' : '此訂單已取消';
  if (display === 'review') return paidUnderReview[locale];
  if (!order || display === 'unknown') return locale === 'ja' ? '店舗に確認してください' : '請洽服務人員確認';
  const fulfillment: Record<GuestOrderStatus['fulfillment_status'], string> = locale === 'ja'
    ? { WAITING: '調理開始待ち', ACCEPTED: '受付済み', PREPARING: '調理中', READY: '配膳待ち', SERVED: '提供済み', CANCELLED: '取消済み' }
    : { WAITING: '等待開始製作', ACCEPTED: '待製作', PREPARING: '製作中', READY: '待送餐', SERVED: '已送達', CANCELLED: '已取消' };
  const payment: Record<GuestOrderStatus['payment_status'], string> = locale === 'ja'
    ? { UNPAID: '未払い', PAID: '支払済み', PENDING: '支払確認中', UNKNOWN: '支払状態を確認中', VOIDED: '未払い取消', REFUND_PENDING: '返金処理中', REFUNDED: '返金済み' }
    : { UNPAID: '尚未付款', PAID: '已付款', PENDING: '付款確認中', UNKNOWN: '付款狀態確認中', VOIDED: '未付款作廢', REFUND_PENDING: '退款處理中', REFUNDED: '已退款' };
  const fulfillmentLabel = order.requires_staff_acceptance && !order.staff_accepted
    ? (locale === 'ja' ? '店員の受付待ち' : '等待店員接單')
    : fulfillment[order.fulfillment_status];
  return `${fulfillmentLabel} · ${payment[order.payment_status]}`;
}

function checkoutStateLabel(state: CheckoutState | string, locale: Locale): string {
  const labels: Record<string, { ja: string; zh: string }> = {
    CREATE_PENDING: { ja: copy.ja.stateCreatePending, zh: copy.zh.stateCreatePending },
    CREATING: { ja: copy.ja.stateCreating, zh: copy.zh.stateCreating },
    OPEN: { ja: copy.ja.stateOpen, zh: copy.zh.stateOpen },
    PROCESSING: { ja: copy.ja.stateProcessing, zh: copy.zh.stateProcessing },
    UNKNOWN: { ja: copy.ja.stateUnknown, zh: copy.zh.stateUnknown },
    PAID: { ja: copy.ja.statePaid, zh: copy.zh.statePaid },
    EXPIRED: { ja: copy.ja.stateExpired, zh: copy.zh.stateExpired },
    FAILED: { ja: copy.ja.stateFailed, zh: copy.zh.stateFailed },
    REVIEW_REQUIRED: { ja: copy.ja.stateReview, zh: copy.zh.stateReview },
  };
  return (labels[state] ?? labels.UNKNOWN!)[locale];
}

export function App({ demo = false }: DemoSurfaceProps) {
  const [locale, setLocale] = useState<Locale>('ja');
  const t = copy[locale];
  const query = useMemo(entryFromURL, []);
  const [entry, setEntry] = useState<Entry | null>(null);
  const entryRef = useRef<Entry | null>(null);
  entryRef.current = entry;
  const mode = entry?.grant.session.mode;
  const [status, setStatus] = useState<PublicTable | null>(null);
  const [preJoinStoreName, setPreJoinStoreName] = useState('');
  useEffect(() => { setPreJoinStoreName(''); }, [query.locator]);
  const [cartState, dispatchCart] = useReducer(guestCartReducer, initialGuestCartState);
  const { cart, preview } = cartState;
  const cartStateRef = useRef(cartState);
  cartStateRef.current = cartState;
  const [sync, dispatchSync] = useReducer(syncReducer, initialSyncState);
  const acceptedSyncSequence = useRef(0);
  const watchdogViewSequence = useRef(0);
  const [watchdogBase, setWatchdogBase] = useState<number | null>(null);
  const syncID = useId();
  const [orders, setOrders] = useState<Order[]>([]);
  const [guestOperations, setGuestOperations] = useState<GuestOperations | null>(null);
  const [operationsStale, setOperationsStale] = useState(false);
  const [checkoutOrderID, setCheckoutOrderID] = useState('');
  const [checkoutPollEpoch, setCheckoutPollEpoch] = useState(0);
  const checkoutOrderRef = useRef('');
  checkoutOrderRef.current = checkoutOrderID;
  const [checkoutStatus, setCheckoutStatus] = useState<CheckoutStatus | null>(null);
  const [checkoutError, setCheckoutError] = useState('');
  const [resumingOrderID, setResumingOrderID] = useState('');
  const [payment, setPayment] = useState<'STRIPE' | 'CASH'>('CASH');
  const [pin, setPin] = useState('');
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [tab, setTab] = useState<'cart' | 'orders'>('cart');
  const [cartOpen, setCartOpen] = useState(false);
  const [cartBarHeight, setCartBarHeight] = useState(0);
  const [optionProductKey, setOptionProductKey] = useState<string | null>(null);
  const menuHeading = useRef<HTMLHeadingElement>(null);
  const orderHeading = useRef<HTMLHeadingElement>(null);
  const entryHeading = useRef<HTMLHeadingElement>(null);
  const pageRestoration = useRef<(() => void) | null>(null);
  const focusPage = useCallback((target: () => HTMLElement | null) => {
    pageRestoration.current?.();
    pageRestoration.current = restoreGuestFocus(target, { from: document.activeElement });
  }, []);
  useEffect(() => () => pageRestoration.current?.(), []);
  useLayoutEffect(() => {
    if (cartBarHeight <= 0) return;
    const root = document.documentElement;
    const previous = root.style.scrollPaddingBottom;
    root.style.scrollPaddingBottom = `calc(${cartBarHeight}px + var(--ref-space-4))`;
    return () => { root.style.scrollPaddingBottom = previous; };
  }, [cartBarHeight]);
  const previousView = useRef<{ active: boolean; tab: 'cart' | 'orders'; sheetOpen: boolean; lineCount: number; menu: GuestMenu | null } | null>(null);
  const menuProducts = useMemo(() => {
    const products = new Map<string, GuestMenu['categories'][number]['products'][number]>();
    for (const category of entry?.menu.categories ?? []) for (const product of category.products) products.set(product.key, product);
    return products;
  }, [entry]);
  const optionProduct = optionProductKey ? menuProducts.get(optionProductKey) ?? null : null;
  const sheetViewActive = !!entry && !blocked && !loading && tab === 'cart';
  const optionSheetOpen = sheetViewActive && !!optionProduct;
  const cartSheetOpen = sheetViewActive && cartOpen;
  const acceptedOperations = useRef(new Map<string, { sessionID: string; token: string; onAccepted: () => void }>());
  const [selections, setSelections] = useState<Record<string, OptionSelection[]>>({});
  const [quantities, setQuantities] = useState<Record<string, number>>({});
  const strings = useRef(t);
  strings.current = t;
  const cartRequestSequence = useRef(0);
  const orderRequestSequence = useRef(0);
  const menuRequestSequence = useRef(0);
  const menuReadsInFlight = useRef(new Set<string>());
  const menuForcedRefreshes = useRef(new Map<string, { entry: Entry; promise: Promise<void>; resolve: () => void }>());
  const refreshMenuRef = useRef<((current: Entry, force?: boolean) => Promise<void>) | null>(null);
  const menuLastReadAt = useRef(new Map<string, number>());
  const checkoutRequestSequence = useRef(0);
  const checkoutViewEpoch = useRef(0);
  const resumeRequestSequence = useRef(0);
  const checkoutReadsInFlight = useRef(new Set<string>());
  const replay = useRef<Replay | null>(null);
  const inFlight = useRef(false);

  const invalidateCheckoutRequests = useCallback(() => {
    checkoutRequestSequence.current++;
  }, []);

  const invalidateGrant = useCallback((sessionID: string) => {
    setPreJoinStoreName('');
    dispatchSync({ type: 'reset', origin: null });
    acceptedSyncSequence.current++;
    cartStateRef.current = initialGuestCartState;
    orderRequestSequence.current++;
    setGuestOperations(null); setOperationsStale(false);
    checkoutRequestSequence.current++;
    checkoutViewEpoch.current++;
    resumeRequestSequence.current++;
    setCheckoutOrderID(''); setCheckoutStatus(null); setCheckoutError(''); setResumingOrderID('');
    sessionStorage.removeItem(storageKey(sessionID));
    setEntry((current) => current?.grant.session.id === sessionID ? null : current);
    dispatchCart({ type: 'session-cleared' }); setOrders([]); setUncertain(false); replay.current = null;
    setBlocked(true); setError('session_expired');
  }, []);

  const adoptCart = useCallback((incoming: Cart, origin?: SyncOrigin) => {
    const next = guestCartReducer(cartStateRef.current, { type: 'cart-received', cart: incoming });
    // Observe batched responses against the same latest cart that dispatch will adopt.
    cartStateRef.current = next;
    const current = entryRef.current;
    if (origin && isOrigin(current, origin) && next.cart === incoming) {
      acceptedSyncSequence.current++;
      dispatchSync({ type: 'success', origin, at: Date.now() });
    }
    dispatchCart({ type: 'cart-received', cart: incoming });
  }, []);

  const confirmDisplayedCart = useCallback((latest: Cart, origin: SyncOrigin) => {
    // Overlapping reads can confirm an identical snapshot without adopting a superseded response.
    const displayed = cartStateRef.current.cart;
    if (isOrigin(entryRef.current, origin) && JSON.stringify(displayed) === JSON.stringify(latest)) {
      acceptedSyncSequence.current++;
      dispatchSync({ type: 'success', origin, at: Date.now() });
    }
  }, []);

  const clearPreview = () => dispatchCart({ type: 'preview-cleared' });

  const refreshCart = useCallback(async (current: Entry) => {
    const sequence = ++cartRequestSequence.current;
    const origin = syncOrigin(current);
    // A read begun before an accepted mutation must not count as a later failure.
    const acceptedAtStart = acceptedSyncSequence.current;
    try {
      const latest = await cartClient.readCart(current.grant.session.id, current.grant.table_join_token);
      if (sequence === cartRequestSequence.current) adoptCart(latest, origin);
      else confirmDisplayedCart(latest, origin);
    } catch (cause) {
      if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) invalidateGrant(current.grant.session.id);
      const active = entryRef.current;
      const syncFailure = cause instanceof ApiError ? cause.status >= 500 && cause.status <= 599
        : cause instanceof Error && cause.message === 'network_unavailable';
      if (sequence === cartRequestSequence.current && acceptedAtStart === acceptedSyncSequence.current
        && isOrigin(active, origin) && syncFailure) {
        dispatchSync({ type: 'read-failure', origin, ...(cause instanceof ApiError ? { status: cause.status } : {}) });
      }
      throw cause;
    }
  }, [adoptCart, confirmDisplayedCart, invalidateGrant]);

  const refreshOrders = useCallback(async (current: Entry) => {
    const sequence = ++orderRequestSequence.current;
    try {
      const latest = await cartClient.orders(current.grant.session.id, current.grant.table_join_token);
      if (sequence === orderRequestSequence.current) setOrders(latest);
      try {
        const operations = await operationsClient.guest(current.grant.session.id, current.grant.table_join_token);
        if (sequence === orderRequestSequence.current) {
          setGuestOperations(operations);
          setOperationsStale(false);
        }
      } catch (cause) {
        if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) invalidateGrant(current.grant.session.id);
        else if (sequence === orderRequestSequence.current) setOperationsStale(true);
      }
    } catch (cause) {
      if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) invalidateGrant(current.grant.session.id);
      else if (sequence === orderRequestSequence.current) setOperationsStale(true);
      throw cause;
    }
  }, [invalidateGrant]);

  const refresh = useCallback(async (current: Entry) => {
    await Promise.all([refreshCart(current), refreshOrders(current)]);
  }, [refreshCart, refreshOrders]);

  const refreshMenu = useCallback(async (current: Entry, force = false) => {
    const sessionID = current.grant.session.id;
    const token = current.grant.table_join_token;
    const readKey = `${sessionID}:${token}`;
    if (menuReadsInFlight.current.has(readKey)) {
      if (!force) return;
      const queued = menuForcedRefreshes.current.get(readKey);
      if (queued) {
        queued.entry = current;
        return queued.promise;
      }
      // A forced read must follow the old request, and that old response is now stale.
      menuRequestSequence.current++;
      let resolve!: () => void;
      const promise = new Promise<void>((done) => { resolve = done; });
      menuForcedRefreshes.current.set(readKey, { entry: current, promise, resolve });
      return promise;
    }
    if (!force && Date.now() - (menuLastReadAt.current.get(readKey) ?? 0) < MENU_REFRESH_TTL_MS) return;
    if (!isCurrentMenuRead(menuRequestSequence.current, menuRequestSequence.current, sessionID, entryRef.current?.grant.session.id, token, entryRef.current?.grant.table_join_token)) return;
    const sequence = ++menuRequestSequence.current;
    menuReadsInFlight.current.add(readKey);
    const isCurrent = () => isCurrentMenuRead(
      sequence,
      menuRequestSequence.current,
      sessionID,
      entryRef.current?.grant.session.id,
      token,
      entryRef.current?.grant.table_join_token,
    );
    try {
      const latest = current.locator
        ? await cartClient.menuForTable(current.locator)
        : await cartClient.menuForSession(sessionID, token);
      if (!isCurrent()) return;
      menuLastReadAt.current.set(readKey, Date.now());
      setEntry((existing) => existing?.grant.session.id === sessionID && existing.grant.table_join_token === token ? { ...existing, menu: latest } : existing);
    } catch (cause) {
      if (!isCurrent()) return;
      menuLastReadAt.current.set(readKey, Date.now());
      if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) invalidateGrant(sessionID);
      else setError((locale === 'ja' ? 'メニューを更新できませんでした。' : '無法更新菜單。'));
    } finally {
      menuReadsInFlight.current.delete(readKey);
      const queued = menuForcedRefreshes.current.get(readKey);
      if (queued) {
        menuForcedRefreshes.current.delete(readKey);
        const followup = refreshMenuRef.current;
        if (!followup) queued.resolve();
        else void followup(queued.entry, true).finally(queued.resolve);
      }
    }
  }, [invalidateGrant, locale]);
  refreshMenuRef.current = refreshMenu;

  const refreshCurrent = async (current: Entry) => {
    await Promise.all([refresh(current), refreshMenu(current, true)]);
  };

  const recoverConflict = async (current: Entry, latest?: Cart) => {
    if (latest) adoptCart(latest, syncOrigin(current));
    else await refresh(current);
    await refreshMenu(current, true);
  };

  const activate = useCallback(async (grant: GuestGrant, locator?: string) => {
    setPreJoinStoreName('');
    dispatchSync({ type: 'reset', origin: { sessionID: grant.session.id, token: grant.table_join_token } });
    acceptedSyncSequence.current++;
    setWatchdogBase(Date.now());
    menuRequestSequence.current++;
    orderRequestSequence.current++;
    setGuestOperations(null); setOperationsStale(false);
    checkoutRequestSequence.current++;
    checkoutViewEpoch.current++;
    resumeRequestSequence.current++;
    setCheckoutOrderID(''); setCheckoutStatus(null); setCheckoutError(''); setResumingOrderID('');
    let menu: GuestMenu;
    try {
      menu = locator
        ? await cartClient.menuForTable(locator)
        : await cartClient.menuForSession(grant.session.id, grant.table_join_token);
    } catch (cause) {
      if (!locator && cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) invalidateGrant(grant.session.id);
      throw cause;
    }
    sessionStorage.setItem(storageKey(grant.session.id), grant.table_join_token);
    const next = { ...(locator ? { locator } : {}), sessionID: grant.session.id, grant, menu };
    menuLastReadAt.current.set(`${grant.session.id}:${grant.table_join_token}`, Date.now());
    entryRef.current = next;
    setEntry(next);
    await refresh(next);
  }, [invalidateGrant, refresh]);

  const refreshCheckoutStatus = useCallback(async (current: Entry, orderID: string): Promise<CheckoutStatus | null> => {
    const readKey = `${current.grant.session.id}:${current.grant.table_join_token}:${orderID}`;
    if (checkoutReadsInFlight.current.has(readKey)) return null;
    checkoutReadsInFlight.current.add(readKey);
    const sequence = ++checkoutRequestSequence.current;
    try {
      const latest = await checkoutClient.status(current.grant.session.id, current.grant.table_join_token, orderID);
      const activeEntry = entryRef.current;
      if (sequence === checkoutRequestSequence.current && checkoutOrderRef.current === orderID && activeEntry?.grant.session.id === current.grant.session.id && activeEntry.grant.table_join_token === current.grant.table_join_token) {
        setCheckoutStatus(latest);
        setCheckoutError('');
        return latest;
      }
      return null;
    } catch (cause) {
      const activeEntry = entryRef.current;
      if (sequence !== checkoutRequestSequence.current || checkoutOrderRef.current !== orderID || activeEntry?.grant.session.id !== current.grant.session.id || activeEntry.grant.table_join_token !== current.grant.table_join_token) return null;
      if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) invalidateGrant(current.grant.session.id);
      else { setCheckoutStatus(null); setCheckoutError(t.checkoutError); }
      return null;
    } finally {
      checkoutReadsInFlight.current.delete(readKey);
    }
  }, [invalidateGrant, t.checkoutError]);

  useEffect(() => {
    if (!entry || !checkoutOrderID || tab !== 'orders') return;
    let timer: number | undefined;
    let disposed = false;
    // REVIEW_REQUIRED is a held state: P80 deliberately stops automated state
    // transitions there until a later staff-resolution workflow is available.
    const isTerminal = (state?: CheckoutState) => state === 'PAID' || state === 'FAILED' || state === 'EXPIRED' || state === 'REVIEW_REQUIRED';
    // Checkout creation usually finishes within seconds; poll quickly until the
    // continuation appears (bounded), then fall back to the slow cadence.
    const isPreparing = (state?: CheckoutState) => state === 'CREATE_PENDING' || state === 'CREATING';
    let fastPolls = 0;
    const schedulePeriodic = (fast = false) => {
      if (!disposed && timer === undefined && document.visibilityState === 'visible') {
        timer = window.setTimeout(() => { timer = undefined; poll(); }, fast ? 2_000 : 20_000);
      }
    };
    const poll = () => {
      void refreshCheckoutStatus(entry, checkoutOrderID).then((latest) => {
        if (disposed) return;
        if (latest && isTerminal(latest.state)) {
          window.clearTimeout(timer);
          timer = undefined;
        } else schedulePeriodic(isPreparing(latest?.state) && fastPolls++ < 15);
      });
    };
    const onVisibilityChange = () => {
      if (timer !== undefined) window.clearTimeout(timer);
      timer = undefined;
      if (document.visibilityState === 'visible') poll();
    };
    onVisibilityChange();
    document.addEventListener('visibilitychange', onVisibilityChange);
    return () => {
      disposed = true;
      document.removeEventListener('visibilitychange', onVisibilityChange);
      if (timer !== undefined) window.clearTimeout(timer);
      invalidateCheckoutRequests();
    };
  }, [checkoutOrderID, checkoutPollEpoch, entry, invalidateCheckoutRequests, refreshCheckoutStatus, tab]);

  const selectCheckoutOrder = (orderID: string) => {
    checkoutViewEpoch.current++;
    resumeRequestSequence.current++;
    if (checkoutOrderRef.current === orderID) {
      setCheckoutStatus(null); setCheckoutError(''); setResumingOrderID('');
      setCheckoutPollEpoch((epoch) => epoch + 1);
      return;
    }
    checkoutOrderRef.current = orderID;
    setCheckoutOrderID(orderID); setCheckoutStatus(null); setCheckoutError(''); setResumingOrderID('');
  };

  const resumeCheckout = async (orderID: string) => {
    const current = entryRef.current;
    if (!current || checkoutOrderRef.current !== orderID || checkoutStatus?.order_id !== orderID || !checkoutStatus.resume_available || resumingOrderID) return;
    const viewEpoch = checkoutViewEpoch.current;
    const requestID = ++resumeRequestSequence.current;
    const isResumeViewCurrent = () => checkoutViewEpoch.current === viewEpoch
      && resumeRequestSequence.current === requestID
      && checkoutOrderRef.current === orderID
      && entryRef.current?.grant.session.id === current.grant.session.id
      && entryRef.current.grant.table_join_token === current.grant.table_join_token;
    setResumingOrderID(orderID); setCheckoutError('');
    try {
      const response = await checkoutClient.resume(current.grant.session.id, orderID);
      const destination = new URL(response.url);
      if (destination.protocol !== 'https:') throw new Error('invalid_checkout_destination');
      const activeEntry = entryRef.current;
      if (isResumeViewCurrent() && activeEntry?.grant.session.id === current.grant.session.id) checkoutNavigation.assign(destination.toString());
    } catch {
      if (isResumeViewCurrent()) {
        setCheckoutError(t.resumeError);
        void refreshCheckoutStatus(current, orderID);
      }
    } finally { if (isResumeViewCurrent()) setResumingOrderID(''); }
  };

  useEffect(() => {
    let active = true;
    const init = async () => {
      try {
        if (query.locator) {
          setPreJoinStoreName('');
          const current = await cartClient.status(query.locator);
          if (!active) return;
          setStatus(current);
          setPreJoinStoreName(current.store_display_name ?? '');
          if (!current.open && current.expired) return;
          if (current.mode === 'DIRECT_SCAN') {
            const grant = await cartClient.joinDirect(query.locator, current.current_session_id ?? '');
            if (active) await activate(grant, query.locator);
          } else if (current.open && current.current_session_id) {
            const saved = sessionStorage.getItem(storageKey(current.current_session_id));
            if (saved) {
              const grant = { table_join_token: saved, expires_at: '', session: { id: current.current_session_id, mode: current.mode } };
              await activate(grant, query.locator);
            }
          }
        } else if (query.sessionID) {
          const saved = sessionStorage.getItem(storageKey(query.sessionID));
          if (saved) {
            const grant = { table_join_token: saved, expires_at: '', session: { id: query.sessionID } };
            await activate(grant);
          }
        }
      } catch (cause) {
        if (active && cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) {
          if (query.sessionID) invalidateGrant(query.sessionID);
          else { setBlocked(true); setError('session_expired'); }
        } else if (active) setError(cause instanceof ApiError && cause.status === 409 ? strings.current.stale : strings.current.error);
      } finally {
        if (active) setLoading(false);
      }
    };
    void init();
    return () => { active = false; };
  }, [activate, invalidateGrant, query.locator, query.sessionID]);

  useEffect(() => {
    if (!entry) return;
    let timer: number | undefined;
    const schedule = () => {
      if (timer !== undefined) window.clearInterval(timer);
      if (document.visibilityState === 'visible') {
        if (tab === 'cart') timer = window.setInterval(() => { void refreshCart(entry).catch(() => undefined); void refreshMenu(entry); }, 2500);
        else timer = window.setInterval(() => { void refreshOrders(entry).catch(() => undefined); }, 20000);
      }
    };
    schedule();
    document.addEventListener('visibilitychange', schedule);
    return () => {
      document.removeEventListener('visibilitychange', schedule);
      if (timer !== undefined) window.clearInterval(timer);
    };
  }, [entry, tab, refreshCart, refreshMenu, refreshOrders]);

  useEffect(() => {
    if (entry && tab === 'cart' && document.visibilityState === 'visible') void refreshCart(entry).catch(() => undefined);
  }, [entry, tab, refreshCart]);

  const syncSessionID = entry?.grant.session.id;
  const syncToken = entry?.grant.table_join_token;
  useEffect(() => {
    if (!syncSessionID || !syncToken) return;
    const origin = { sessionID: syncSessionID, token: syncToken };
    const offline = () => dispatchSync({ type: 'offline', origin });
    const online = () => dispatchSync({ type: 'online', origin });
    window.addEventListener('offline', offline);
    window.addEventListener('online', online);
    return () => {
      window.removeEventListener('offline', offline);
      window.removeEventListener('online', online);
    };
  }, [syncSessionID, syncToken]);
  useEffect(() => {
    const visibility = () => {
      watchdogViewSequence.current++;
      setWatchdogBase(syncSessionID && syncToken && tab === 'cart' && document.visibilityState === 'visible' ? Date.now() : null);
    };
    visibility();
    document.addEventListener('visibilitychange', visibility);
    return () => document.removeEventListener('visibilitychange', visibility);
  }, [syncSessionID, syncToken, tab]);
  useEffect(() => {
    if (!syncSessionID || !syncToken || tab !== 'cart' || watchdogBase === null || sync.degraded || document.visibilityState !== 'visible') return;
    const deadline = Math.max(watchdogBase, sync.lastSyncedAt ?? watchdogBase) + 7500;
    const acceptedAtSchedule = acceptedSyncSequence.current;
    const viewAtSchedule = watchdogViewSequence.current;
    const timer = window.setTimeout(() => {
      if (document.visibilityState === 'visible' && acceptedAtSchedule === acceptedSyncSequence.current && viewAtSchedule === watchdogViewSequence.current) {
        dispatchSync({ type: 'watchdog', origin: { sessionID: syncSessionID, token: syncToken } });
      }
    }, Math.max(0, deadline - Date.now()));
    return () => window.clearTimeout(timer);
  }, [syncSessionID, syncToken, tab, watchdogBase, sync]);

  useEffect(() => { document.documentElement.lang = locale === 'ja' ? 'ja' : 'zh-Hant'; }, [locale]);
  useEffect(() => {
    guestSurfaceMounts++;
    document.documentElement.setAttribute('data-surface', 'guest');
    return () => {
      guestSurfaceMounts--;
      if (guestSurfaceMounts === 0) document.documentElement.removeAttribute('data-surface');
    };
  }, []);
  useEffect(() => {
    if (!entry || tab === 'orders') {
      setCartOpen(false);
      setOptionProductKey(null);
    }
  }, [entry, tab]);
  useEffect(() => {
    if (optionProductKey && !optionProduct) setOptionProductKey(null);
  }, [optionProductKey, optionProduct]);
  useLayoutEffect(() => {
    const previous = previousView.current;
    const active = !!entry && !blocked && !loading;
    const lineCount = cart?.lines.length ?? 0;
    const sheetOpen = cartSheetOpen || optionSheetOpen;
    previousView.current = { active, tab, sheetOpen, lineCount, menu: entry?.menu ?? null };
    if (sheetOpen) { pageRestoration.current?.(); return; }
    // An open sheet restores focus after its exit; page feedback has no overlay lifecycle.
    if (!previous?.active || previous.sheetOpen || document.querySelector('[data-guest-sheet]')) return;
    if (!active) focusPage(() => entryHeading.current);
    else if (tab === 'orders' && previous.tab !== 'orders') focusPage(() => orderHeading.current);
    else if (tab === 'cart' && !sheetOpen && document.activeElement === document.body
      && ((previous.lineCount > 0 && lineCount === 0) || previous.menu !== entry?.menu)) focusPage(() => menuHeading.current);
  }, [entry, blocked, loading, tab, cartSheetOpen, optionSheetOpen, cart, focusPage]);
  useEffect(() => {
    if (demo) {
      if (payment === 'STRIPE') setPayment('CASH');
      return;
    }
    if (!guestOperations) return;
    const cardEnabled = !!guestOperations.card_enabled;
    if (!cardEnabled && payment === 'STRIPE') setPayment('CASH');
    else if (cardEnabled && !guestOperations.cash_enabled && payment === 'CASH') setPayment('STRIPE');
  }, [demo, guestOperations, payment]);
  // Only a method the store offers may be committed, even in the render before
  // the effect above settles the stored choice.
  const cardEnabled = !demo && !!guestOperations?.card_enabled;
  const cashEnabled = !!guestOperations?.cash_enabled;
  const paymentMethod: 'STRIPE' | 'CASH' = !cardEnabled ? 'CASH' : !cashEnabled ? 'STRIPE' : payment;
  useEffect(() => {
    if (preview && cart && (preview.generation !== cart.generation || preview.version !== cart.version)) {
      dispatchCart({ type: 'preview-cleared' });
      setError(t.stale);
    }
  }, [cart, preview, t.stale]);

  const submitJoin = async () => {
    setBusy(true); setError('');
    try {
      if (query.locator) {
        let current = status;
        if (!current) {
          setPreJoinStoreName('');
          current = await cartClient.status(query.locator);
          setPreJoinStoreName(current.store_display_name ?? '');
        }
        setStatus(current);
        if (!current.open && current.mode === 'DIRECT_SCAN') {
          const grant = await cartClient.joinDirect(query.locator, '');
          await activate(grant, query.locator);
        } else if (current.open && current.current_session_id) {
          const joined = current.mode === 'DIRECT_SCAN'
            ? await cartClient.joinDirect(query.locator, current.current_session_id)
            : await cartClient.joinSession(current.current_session_id, current.current_session_id, pin);
          await activate(joined, query.locator);
        } else {
          setError(t.closed);
        }
      } else if (query.sessionID) {
        const grant = await cartClient.joinSession(query.sessionID, query.sessionID, pin);
        await activate(grant);
      }
    } catch (cause) {
      if (cause instanceof ApiError && (cause.body.error === 'invalid_pin' || cause.body.error === 'pin_locked')) setError(locale === 'ja' ? 'PINを確認してください。' : '請確認桌次 PIN。');
      else setError(cause instanceof ApiError && cause.status === 409 ? t.stale : t.error);
    } finally { setBusy(false); }
  };

  const acceptOperation = (key: string) => {
    const accepted = acceptedOperations.current.get(key);
    acceptedOperations.current.delete(key);
    if (accepted && isOrigin(entryRef.current, accepted)) accepted.onAccepted();
  };

  const applyMutation = async (run: (key: string) => Promise<Cart>, onAccepted?: () => void) => {
    if (!entry || !cart || inFlight.current || uncertain) return;
    inFlight.current = true; setBusy(true); setError(''); clearPreview(); setNotice('');
    const key = newOperationKey();
    if (onAccepted) acceptedOperations.current.set(key, { sessionID: entry.grant.session.id, token: entry.grant.table_join_token, onAccepted });
    const origin = syncOrigin(entry);
    const operation = run(key);
    replay.current = { key, origin, run: () => run(key) };
    try {
      const updated = await operation;
      adoptCart(updated, origin); acceptOperation(key); replay.current = null; setUncertain(false);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        if (entry) await recoverConflict(entry, cause.body.cart);
        setError(cause.body.error === 'unavailable' ? t.unavailableLines : t.stale);
        acceptedOperations.current.delete(key); replay.current = null;
      } else if (cause instanceof ApiError && cause.status >= 500) {
        setUncertain(true); setError(t.network);
      } else if (cause instanceof ApiError) {
        if (cause.status === 403 || cause.status === 410) invalidateGrant(entry.grant.session.id);
        else setError(t.error);
        acceptedOperations.current.delete(key); replay.current = null;
      } else {
        setUncertain(true); setError(t.network);
      }
    } finally { inFlight.current = false; setBusy(false); }
  };

  const retryUncertain = async () => {
    const pending = replay.current;
    if (!pending || inFlight.current) return;
    inFlight.current = true; setBusy(true); setError('');
    try {
      const result = await pending.run();
      if ('outcome' in result) {
        if (result.order_read_grant) rememberOrderGrant(result.order_read_grant);
        adoptCart(result.cart, pending.origin);
        setOrders((current) => [result.order, ...current.filter((order) => order.order_id !== result.order.order_id)]);
        clearPreview(); setTab('orders'); setNotice(result.outcome === 'ALREADY_COMMITTED' ? t.already : t.order);
      } else {
        adoptCart(result, pending.origin); acceptOperation(pending.key); setNotice(t.latest);
      }
      replay.current = null; setUncertain(false);
      if (entry) await refresh(entry);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        if (entry) await recoverConflict(entry, cause.body.cart);
        acceptedOperations.current.delete(pending.key); replay.current = null; setUncertain(false); setError(t.stale);
      } else if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) {
        if (entry) invalidateGrant(entry.grant.session.id);
      }
      else setError(t.network);
    } finally { inFlight.current = false; setBusy(false); }
  };

  const chooseOption = (productKey: string, groupKey: string, optionKey: string, max: number) => {
    setSelections((current) => {
      const selected = current[productKey] ?? [];
      const exists = selected.some((item) => item.group_key === groupKey && item.option_key === optionKey);
      const inGroup = selected.filter((item) => item.group_key === groupKey);
      const next = exists
        ? selected.filter((item) => !(item.group_key === groupKey && item.option_key === optionKey))
        : [...selected.filter((item) => item.group_key !== groupKey), ...inGroup.slice(0, Math.max(0, max - 1)), { group_key: groupKey, option_key: optionKey }];
      return { ...current, [productKey]: next };
    });
  };

  const openPreview = async () => {
    if (!entry || !cart || cart.lines.length === 0 || busy || uncertain) return;
    setBusy(true); setError(''); clearPreview();
    try {
      const latest = await cartClient.preview(entry.grant.session.id, entry.grant.table_join_token, cart);
      if (latest.generation !== cart.generation || latest.version !== cart.version) {
        await Promise.all([refresh(entry), refreshMenu(entry, true)]); setError(t.stale); return;
      }
      dispatchCart({ type: 'preview-received', requested: cart, preview: latest });
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        await recoverConflict(entry, cause.body.cart);
        setError(t.stale);
      } else if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) invalidateGrant(entry.grant.session.id);
      else {
        if (cause instanceof ApiError && cause.body.error === 'unavailable') await refreshMenu(entry, true);
        setError(cause instanceof ApiError && cause.body.error === 'unavailable' ? t.unavailableLines : t.error);
      }
    } finally { setBusy(false); }
  };

  const confirmOrder = async () => {
    if (!entry || !cart || !preview || preview.version !== cart.version || preview.generation !== cart.generation || busy) return;
    const origin = syncOrigin(entry);
    setBusy(true); setError(''); setNotice('');
    const key = newOperationKey();
    const method = paymentMethod;
    if (method === 'STRIPE') {
      try {
        await checkoutClient.ensureContext(entry.grant.session.id, entry.grant.table_join_token);
      } catch {
        setError(t.checkoutUnavailable); setBusy(false); return;
      }
      const activeEntry = entryRef.current;
      if (activeEntry?.grant.session.id !== entry.grant.session.id || activeEntry.grant.table_join_token !== entry.grant.table_join_token) {
        setBusy(false); return;
      }
    }
    try {
      const result = await cartClient.commit(entry.grant.session.id, entry.grant.table_join_token, preview, method, key);
      if (result.order_read_grant) rememberOrderGrant(result.order_read_grant);
      adoptCart(result.cart, origin); setOrders((current) => [result.order, ...current.filter((order) => order.order_id !== result.order.order_id)]);
      selectCheckoutOrder(method === 'STRIPE' ? result.order.order_id : '');
      clearPreview(); setTab('orders'); setNotice(result.outcome === 'ALREADY_COMMITTED' ? t.already : t.order);
      await refresh(entry);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        await recoverConflict(entry, cause.body.cart);
        clearPreview(); setError(cause.body.error === 'unavailable' ? t.unavailableLines : t.stale);
      } else if (cause instanceof ApiError && cause.status >= 500) {
        setError(t.network); setUncertain(true);
        replay.current = { ...createReplayOperation(key, { preview, payment: method }, (original) => cartClient.commit(entry.grant.session.id, entry.grant.table_join_token, original.preview, original.payment, key)), origin };
      } else if (cause instanceof ApiError) {
        clearPreview();
        if (cause.status === 403 || cause.status === 410) invalidateGrant(entry.grant.session.id);
        else {
          if (cause.body.error === 'unavailable' || cause.body.error === 'requote_required') await refreshMenu(entry, true);
          setError(cause.body.error === 'unavailable' ? t.unavailableLines : t.error);
        }
      }
      else {
        setError(t.network); setUncertain(true);
        replay.current = { ...createReplayOperation(key, { preview, payment: method }, (original) => cartClient.commit(entry.grant.session.id, entry.grant.table_join_token, original.preview, original.payment, key)), origin };
      }
    } finally { setBusy(false); }
  };

  const syncTime = sync.lastSyncedAt === null ? '—' : new Intl.DateTimeFormat('ja-JP', { timeZone: 'Asia/Tokyo', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(sync.lastSyncedAt);
  const syncReason = t.syncReason.replace('{time}', syncTime);
  const syncNotice = (id: string) => sync.degraded ? <div id={id} className={guestStyles.syncNotice} role="status"><strong>{t.syncUnstable}</strong>{syncReason}</div> : null;
  const storeName = demo ? demoStoreName : entry ? (entry.menu.store_display_name ?? '') : preJoinStoreName;

  if (loading) return <Shell demo={demo} storeName={storeName} locale={locale} setLocale={setLocale} t={t} tableLabel={query.locator ? `${t.table} ${displayTableLocator(query.locator, demo)}` : t.table}><div className="loading-card" role="status">{t.loading}</div>{syncNotice(`${syncID}-page`)}</Shell>;
  const visibleError = error === 'session_expired' ? t.closed : error;
  if (!entry || blocked) return <Shell demo={demo} storeName={storeName} locale={locale} setLocale={setLocale} t={t} tableLabel={query.locator ? `${t.table} ${displayTableLocator(query.locator, demo)}` : t.table}>
    <section className="entry-card" aria-labelledby="entry-title">
      <h1 id="entry-title" ref={entryHeading} tabIndex={-1}>{t.join}</h1><p>{t.joinHint}</p>
      <div className="table-chip"><span>{t.table}</span><strong>{query.locator ? displayTableLocator(query.locator, demo) : query.sessionID ?? '—'}</strong></div>
      {status?.expired && <p className="notice">{t.closed}</p>}
      {(!query.locator || status?.mode === 'SESSION_PIN') && <label className="field">{t.pin}<input className={guestStyles.entryControl} data-control="guest-entry" autoComplete="one-time-code" inputMode="numeric" value={pin} onChange={(event) => setPin(event.target.value)} /></label>}
      {(!query.locator && !query.sessionID) && <p className="notice">テーブル QR からアクセスしてください。</p>}
      <button className={`button primary wide ${guestStyles.entryControl}`} data-control="guest-entry" disabled={busy || blocked || (!query.locator && !query.sessionID) || !!status?.expired} onClick={() => void submitJoin()}>{busy ? t.loading : t.enter}</button>
      {visibleError && <p className="error" role="alert">{visibleError}</p>}
    </section>
  </Shell>;

  const previewMatchesCart = !!preview && !!cart && preview.generation === cart.generation && preview.version === cart.version;
  const unavailable = cart?.lines.some((line) => !line.product_available || line.options.some((option) => !option.available) || !menuProducts.get(line.product_key)?.selectable || menuProducts.get(line.product_key)?.company_emergency_hold_active) ?? false;
  const previewAmountsSafe = !!preview && isSafeAmount(preview.quote.tax.total_gross_jpy) && isSafeAmount(preview.quote.tax.total_tax_jpy) && preview.quote.lines.every((line) => isSafeAmount(line.base_gross_jpy) && line.options.every((option) => isSafeAmount(option.price_gross_jpy)));

  return <Shell demo={demo} storeName={storeName} locale={locale} setLocale={setLocale} t={t} cartBarHeight={cartBarHeight} tableLabel={query.locator ? `${t.table} ${displayTableLocator(query.locator, demo)}` : t.table}>
    <div className={guestStyles.workspace}>
      <header className={guestStyles.welcome}>
        <div><h1 ref={menuHeading} tabIndex={-1}>{t.menu}</h1><p>{mode === 'DIRECT_SCAN' ? t.directMode : mode === 'SESSION_PIN' ? t.pinMode : mode === 'STAFF_OPEN' ? t.staffMode : t.tableActive}</p></div>
        {tab === 'cart' ? <button className={guestStyles.navigation} onClick={() => { setTab('orders'); clearPreview(); }}>{t.committed}（{orders.length}）</button>
          : <button className={guestStyles.navigation} onClick={() => { setTab('cart'); clearPreview(); focusPage(() => menuHeading.current); }}>{t.backToMenu}</button>}
      </header>
      {syncNotice(`${syncID}-page`)}
      {tab === 'cart' && <>
        <CategoryNav categories={entry.menu.categories} locale={locale} />
        <MenuList categories={entry.menu.categories} locale={locale} busy={busy} uncertain={uncertain}
          onOpenOptions={(product) => setOptionProductKey(product.key)}
          onAdd={(product) => void applyMutation((key) => cartClient.addLine(entry.grant.session.id, entry.grant.table_join_token, cart!, product.key, 1, [], key))} />
        <OptionSheet product={optionProduct} isOpen={optionSheetOpen} onOpenChange={(open) => { if (!open) setOptionProductKey(null); }}
          locale={locale} selected={selections[optionProductKey ?? ''] ?? []} quantity={quantities[optionProductKey ?? ''] ?? 1}
          notice={<DemoNotice demo={demo} locale={locale} />} busy={busy} uncertain={uncertain} fallbackFocus={() => entryHeading.current ?? orderHeading.current ?? menuHeading.current}
          onChoose={(groupKey, optionKey, max) => { if (optionProduct) chooseOption(optionProduct.key, groupKey, optionKey, max); }}
          onClear={(groupKey) => { if (optionProduct) setSelections((current) => ({ ...current, [optionProduct.key]: (current[optionProduct.key] ?? []).filter((item) => item.group_key !== groupKey) })); }}
          onQuantity={(quantity) => { if (optionProduct) setQuantities((current) => ({ ...current, [optionProduct.key]: quantity })); }}
          onAdd={() => { if (optionProduct) void applyMutation((key) => cartClient.addLine(entry.grant.session.id, entry.grant.table_join_token, cart!, optionProduct.key, quantities[optionProduct.key] ?? 1, selections[optionProduct.key] ?? [], key), () => setOptionProductKey(null)); }}>
          {syncNotice(`${syncID}-option`)}
          {uncertain && <button className={guestStyles.navigation} disabled={busy} onClick={() => void retryUncertain()}>{t.retry}</button>}
          {visibleError && <p role="alert" className={guestStyles.error}>{visibleError}</p>}
          {notice && <p role="status">{notice}</p>}
        </OptionSheet>

        <CartBar active={tab === 'cart'} cart={cart} menu={entry.menu} locale={locale} onOpen={() => setCartOpen(true)} onHeightChange={setCartBarHeight}
          sync={sync.degraded ? `${t.syncWaiting}・${syncTime} ${t.syncAt}` : `${t.synced}・${syncTime}`} />
        <CartSheet isOpen={cartSheetOpen} onOpenChange={setCartOpen} cart={cart} preview={preview} products={menuProducts}
          locale={locale} demo={demo} demoNotice={<DemoNotice demo={demo} locale={locale} />}
          degraded={sync.degraded} syncNotice={syncNotice(`${syncID}-cart`)} syncReasonID={`${syncID}-cart`} payment={paymentMethod} cashEnabled={!!guestOperations?.cash_enabled} cardEnabled={!demo && !!guestOperations?.card_enabled}
          onPayment={setPayment} busy={busy} uncertain={uncertain} unavailable={unavailable} previewMatchesCart={previewMatchesCart} previewAmountsSafe={previewAmountsSafe}
          error={visibleError} notice={notice} onPreview={() => void openPreview()} onClearPreview={clearPreview} onInvalidated={() => setError((current) => current || t.stale)} onConfirm={() => void confirmOrder()}
          onRefresh={() => void refreshCurrent(entry)} onRetry={() => void retryUncertain()}
          onRemove={(line) => void applyMutation((key) => cartClient.removeLine(entry.grant.session.id, entry.grant.table_join_token, cart!, line.line_id, key))}
          onSetLine={(line, quantity, options) => void applyMutation((key) => cartClient.setLine(entry.grant.session.id, entry.grant.table_join_token, cart!, line.line_id, quantity, options, key))}
          fallbackFocus={() => entryHeading.current ?? orderHeading.current ?? menuHeading.current} />
        {!cartOpen && !optionProduct && <div className={guestStyles.feedback} style={cartBarHeight > 0 ? { bottom: `calc(${cartBarHeight}px + var(--ref-space-2))` } : undefined}>
          {uncertain && <button className={guestStyles.navigation} disabled={busy} onClick={() => void retryUncertain()}>{t.retry}</button>}
          {visibleError && <p role="alert" className={guestStyles.error}>{visibleError}</p>}
          {notice && <p role="status">{notice}</p>}
        </div>}
      </>}

      {tab === 'orders' && <section className={guestStyles.orders}><div className="section-heading"><div><h2 ref={orderHeading} tabIndex={-1}>{t.committed}</h2></div><button className="icon-button" aria-label={t.refresh} onClick={() => void refreshCurrent(entry)}>↻</button></div>
        {notice && <p role="status">{notice}</p>}
        {operationsStale && <p className="notice" role="status">{guestOperations ? t.operationsStale : t.amountUnavailable}</p>}
        {orders.length === 0 ? <div className="empty-state"><div className="empty-icon">✓</div><p>{t.noOrders}</p></div> : orders.map((order) => { const operations = guestOperations?.orders.find((item) => item.id === order.order_id); return <article className="order-card" data-order-card={order.order_id} key={order.order_id}>
          <RequestOrderLink sessionID={entry.grant.session.id} token={entry.grant.table_join_token} orderID={order.order_id} locale={locale} />
          <div className="order-card-title"><div><span className="order-kicker">{order.requires_staff_acceptance ? t.staffWaiting : t.acceptedOrder} · #{order.generation}</span><h3>{order.order_id}</h3></div><span className="order-method">{order.payment_method === 'STRIPE' ? t.stripe : demo ? t.demoCash : t.cash}</span></div>
          <OrderTracks order={operations} paymentMethod={order.payment_method}
            checkout={checkoutStatus?.order_id === order.order_id ? checkoutStatus : null}
            operationsStale={operationsStale} locale={locale} statusLabel={guestOrderLabel(operations, locale)} />
          {order.payment_method === 'STRIPE' && !operationsStale && operations && guestDisplayKind(operations) === 'standard' && <div className="checkout-status-panel" data-checkout-panel>
            <strong>{t.checkoutStatus}</strong>
            {checkoutOrderID === order.order_id && <>
              {checkoutStatus?.order_id === order.order_id && <p className="guest-order-status" data-checkout-status>{checkoutStateLabel(checkoutStatus.state, locale)}</p>}
              {!checkoutStatus && !checkoutError && <p className="muted" role="status">{t.checkoutLoading}</p>}
              {checkoutError && <p className="error" role="alert">{checkoutError}</p>}
              {checkoutStatus?.order_id === order.order_id && checkoutStatus.resume_available && <button className="button small primary" disabled={resumingOrderID === order.order_id} onClick={() => void resumeCheckout(order.order_id)}>{resumingOrderID === order.order_id ? t.checkoutLoading : t.continueCheckout}</button>}
              {checkoutStatus?.order_id === order.order_id && !checkoutStatus.resume_available && !['PAID', 'EXPIRED', 'FAILED'].includes(checkoutStatus.state) && <p className="notice">{t.checkoutSupport}</p>}
            </>}
            <button className="button small quiet" onClick={() => selectCheckoutOrder(order.order_id)}>{t.checkCheckout}</button>
          </div>}
          {operations?.adjusted && <p className="notice" role="status">{t.changed}</p>}
          {(operations?.effective_lines ?? []).map((line, index) => <div className="order-line" key={`${line.product_name}-${index}`}><span>{line.product_name} × {line.quantity}{(line.option_names ?? []).map((name, optionIndex) => <small className="order-option" key={`${optionIndex}-${name}`}>{name}</small>)}</span><strong>{safeAmount(line.line_total_jpy)}</strong></div>)}
          {operations ? <div className="order-total"><span>{t.total}</span><strong>{safeAmount(operations.effective_total_gross_jpy)}</strong></div> : <div className="order-total"><span>{t.total}</span><strong>{t.amountUnavailable}</strong></div>}
          {operations?.adjusted && <div className="tax-row"><span>{t.originalTotal}</span><span>{safeAmount(operations.total_gross_jpy)}</span></div>}
          {(operations?.refunds ?? []).map((refund, index) => { const kind = guestRefundKind(refund); return <p className="guest-order-status" role="status" key={`${refund.status}-${index}`}>{kind === 'processing' ? t.refundPending : kind === 'succeeded' ? `${t.refunded} ${safeAmount(refund.amount_jpy)}` : t.refundFailed}</p>; })}
          {operations && isPositiveAmount(operations.refunded_total_jpy) && <div className="tax-row"><span>{t.refundedTotal}</span><span>{safeAmount(operations.refunded_total_jpy)}</span></div>}
          <small>{new Date(order.created_at).toLocaleString(locale === 'ja' ? 'ja-JP' : 'zh-TW')}</small>
        </article>; })}
      </section>}
    </div>
  </Shell>;
}

function Shell({ children, demo = false, storeName, locale, setLocale, t, tableLabel, cartBarHeight = 0 }: { children: React.ReactNode; demo?: boolean; storeName: string; locale: Locale; setLocale: (locale: Locale) => void; t: Record<string, string>; tableLabel: string; cartBarHeight?: number }) {
  return <main className={guestStyles.shell} data-surface="guest" style={cartBarHeight > 0 ? { paddingBottom: `calc(${cartBarHeight}px + var(--ref-space-4))` } : undefined}><meta name="referrer" content="no-referrer" /><header className={guestStyles.topbar}><div className={guestStyles.identity}>{storeName && <strong data-store-slot={demo ? 'demo' : 'api'}>{storeName}</strong>}<span className={guestStyles.tableLabel}>{tableLabel}</span></div><button className={guestStyles.navigation} lang={locale === 'ja' ? 'zh-Hant' : 'ja'} onClick={() => setLocale(locale === 'ja' ? 'zh' : 'ja')}>{t.locale}</button></header><DemoBanner demo={demo} locale={locale} />{children}<MyOrders locale={locale} /><footer className={guestStyles.footer}>Powered by Tojima</footer></main>;
}
