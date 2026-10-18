import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react';
import { guestDisplayKind, guestRefundKind, paidUnderReview } from './guest-display';
import { ApiError, amountJPY, cartClient, newOperationKey } from '../api/client';
import type { Cart, CartLine, GuestGrant, GuestMenu, OptionSelection, Order, PublicTable } from '../api/types';
import { operationsClient } from '../api/operations';
import type { GuestOperations, GuestOrderStatus } from '../api/operations';
import { checkoutClient, checkoutNavigation } from '../api/checkout';
import type { CheckoutState, CheckoutStatus } from '../api/checkout';
import { createReplayOperation, guestCartReducer, initialGuestCartState } from './cart-state';
import { DemoBanner } from './DemoBanner';
import type { DemoSurfaceProps } from './DemoBanner';

import { MyOrders, RequestOrderLink } from './OrderRead';
import { rememberOrderGrant } from './my-orders';

type Locale = 'ja' | 'zh';
const MENU_REFRESH_TTL_MS = 15_000;
const copy = {
  ja: {
    brand: '東島', subtitle: 'TABLE ORDER', table: 'テーブル', pending: '共有カート', committed: '注文履歴',
    join: 'ご注文を始める', joinHint: 'テーブルのご案内に従って参加してください。', pin: 'テーブル PIN', enter: '参加する',
    loading: '確認しています…', closed: 'このテーブルは現在ご利用いただけません。スタッフにお声がけください。',
    menu: 'お品書き', available: '注文できます', unavailable: '現在注文できません', pendingAvailability: '店舗確認中', emergencyHold: '販売停止中',
    choose: '選択してください', required: '必須', noOption: '選択しない', add: 'カートに追加', cartEmpty: 'カートはまだ空です。',
    remove: '削除', quantity: '数量', options: '追加オプション', latest: '最新の共有内容', tableActive: 'こちらのテーブルで注文中', directMode: 'テーブルの QR から参加', pinMode: 'テーブル PIN で参加', staffMode: 'スタッフが開始したテーブル',
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
    error: '処理できませんでした。内容を確認してもう一度お試しください。', unavailableLines: '注文できない商品があります。削除または内容を変更してから確認してください。', amountError: '金額を安全に表示できません。注文を続けられません。',
    selectRequired: '必須オプションを選択してください。', itemMissing: 'メニューで確認できない商品です。削除してください。',
    operationsStale: '最新の状況を確認できません。表示は前回確認時の内容です。', amountUnavailable: '最新の注文内容と金額を確認できません。', changed: '店舗により注文内容が変更されました', cancelled: 'この注文は取り消されました', originalTotal: '注文確定時の税込合計', refundPending: '返金処理中', refundFailed: '返金を確認中です。店舗にお問い合わせください', refunded: '返金済み', refundedTotal: '返金合計',
    noOrders: '確定済みの注文はありません。', direct: 'QRテーブルを確認', session: 'セッションから再参加', locale: '繁中', demoCash: '現金（デモ・テスト専用）',
  },
  zh: {
    brand: '東島', subtitle: '桌邊點餐', table: '桌次', pending: '共用購物車', committed: '已送出訂單',
    join: '開始點餐', joinHint: '請依桌邊指示加入本桌。', pin: '桌次 PIN', enter: '加入桌次',
    loading: '正在確認…', closed: '目前無法使用此桌，請洽服務人員。',
    menu: '菜單', available: '可點選', unavailable: '目前無法點選', pendingAvailability: '門店確認中', emergencyHold: '暫停販售',
    choose: '請選擇', required: '必選', noOption: '不選擇', add: '加入購物車', cartEmpty: '購物車目前是空的。',
    remove: '移除', quantity: '數量', options: '加料選項', latest: '最新共用內容', tableActive: '正在此桌點餐', directMode: '透過桌邊 QR 加入', pinMode: '透過桌次 PIN 加入', staffMode: '由服務人員開啟桌次',
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
    error: '暫時無法處理，請確認內容後重試。', unavailableLines: '有無法點餐的品項，請移除或調整後再確認。', amountError: '無法安全顯示金額，暫時不能繼續送單。',
    selectRequired: '請選擇必選項目。', itemMissing: '菜單中找不到此商品，請移除。',
    operationsStale: '無法確認最新狀態，目前顯示上次確認的內容。', amountUnavailable: '目前無法確認最新訂單內容與金額。', changed: '店家已調整您的訂單', cancelled: '此訂單已取消', originalTotal: '送單時含稅總額', refundPending: '退款處理中', refundFailed: '退款確認中，請洽店家', refunded: '已退款', refundedTotal: '退款總額',
    noOrders: '目前沒有已確認訂單。', direct: '確認 QR 桌次', session: '使用桌次重新加入', locale: '日本語', demoCash: '現金（展示測試專用）',
  },
} satisfies Record<Locale, Record<string, string>>;

interface Entry {
  locator?: string;
  sessionID?: string;
  grant: GuestGrant;
  menu: GuestMenu;
}
interface Replay { key: string; run: () => Promise<Cart | import('../api/types').CommitResult>; }

function entryFromURL(): { locator?: string; sessionID?: string } {
  const query = new URLSearchParams(window.location.search);
  const locator = query.get('table')?.trim();
  const sessionID = query.get('session')?.trim();
  return { ...(locator ? { locator } : {}), ...(sessionID ? { sessionID } : {}) };
}

export function displayTableLocator(locator: string, demo: boolean): string {
  return demo && locator === 'demo-shironagasukujira-table-05' ? '05' : locator;
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
    ? { UNPAID: '未払い', PAID: '支払済み', PENDING: '支払確認中', UNKNOWN: '支払状態を確認中' }
    : { UNPAID: '尚未付款', PAID: '已付款', PENDING: '付款確認中', UNKNOWN: '付款狀態確認中' };
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
  const [{ cart, preview }, dispatchCart] = useReducer(guestCartReducer, initialGuestCartState);
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
  const [payment, setPayment] = useState<'STRIPE' | 'CASH'>(demo ? 'CASH' : 'STRIPE');
  const [pin, setPin] = useState('');
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [tab, setTab] = useState<'cart' | 'orders'>('cart');
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

  const adoptCart = useCallback((incoming: Cart) => {
    dispatchCart({ type: 'cart-received', cart: incoming });
  }, []);

  const clearPreview = () => dispatchCart({ type: 'preview-cleared' });

  const refreshCart = useCallback(async (current: Entry) => {
    const sequence = ++cartRequestSequence.current;
    try {
      const latest = await cartClient.readCart(current.grant.session.id, current.grant.table_join_token);
      if (sequence === cartRequestSequence.current) adoptCart(latest);
    } catch (cause) {
      if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) invalidateGrant(current.grant.session.id);
      throw cause;
    }
  }, [adoptCart, invalidateGrant]);

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
    if (latest) adoptCart(latest);
    else await refresh(current);
    await refreshMenu(current, true);
  };

  const activate = useCallback(async (grant: GuestGrant, locator?: string) => {
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
    const schedulePeriodic = () => {
      if (!disposed && timer === undefined && document.visibilityState === 'visible') {
        timer = window.setInterval(poll, 20_000);
      }
    };
    const poll = () => {
      void refreshCheckoutStatus(entry, checkoutOrderID).then((latest) => {
        if (disposed) return;
        if (latest && isTerminal(latest.state)) {
          window.clearInterval(timer);
          timer = undefined;
        } else schedulePeriodic();
      });
    };
    const onVisibilityChange = () => {
      if (timer !== undefined) window.clearInterval(timer);
      timer = undefined;
      if (document.visibilityState === 'visible') poll();
    };
    onVisibilityChange();
    document.addEventListener('visibilitychange', onVisibilityChange);
    return () => {
      disposed = true;
      document.removeEventListener('visibilitychange', onVisibilityChange);
      if (timer !== undefined) window.clearInterval(timer);
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
          const current = await cartClient.status(query.locator);
          if (!active) return;
          setStatus(current);
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

  useEffect(() => { document.documentElement.lang = locale === 'ja' ? 'ja' : 'zh-Hant'; }, [locale]);
  useEffect(() => {
    if (demo && payment === 'STRIPE') setPayment('CASH');
    else if (guestOperations?.cash_enabled === false && payment === 'CASH' && !demo) setPayment('STRIPE');
  }, [demo, guestOperations?.cash_enabled, payment]);
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
        const current = status ?? await cartClient.status(query.locator);
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

  const applyMutation = async (run: (key: string) => Promise<Cart>) => {
    if (!entry || !cart || inFlight.current || uncertain) return;
    inFlight.current = true; setBusy(true); setError(''); clearPreview(); setNotice('');
    const key = newOperationKey();
    const operation = run(key);
    replay.current = { key, run: () => run(key) };
    try {
      const updated = await operation;
      adoptCart(updated); replay.current = null; setUncertain(false);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        if (entry) await recoverConflict(entry, cause.body.cart);
        setError(cause.body.error === 'unavailable' ? t.unavailableLines : t.stale);
        replay.current = null;
      } else if (cause instanceof ApiError && cause.status >= 500) {
        setUncertain(true); setError(t.network);
      } else if (cause instanceof ApiError) {
        if (cause.status === 403 || cause.status === 410) invalidateGrant(entry.grant.session.id);
        else setError(t.error);
        replay.current = null;
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
        adoptCart(result.cart);
        setOrders((current) => [result.order, ...current.filter((order) => order.order_id !== result.order.order_id)]);
        clearPreview(); setTab('orders'); setNotice(result.outcome === 'ALREADY_COMMITTED' ? t.already : t.order);
      } else {
        adoptCart(result); setNotice(t.latest);
      }
      replay.current = null; setUncertain(false);
      if (entry) await refresh(entry);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        if (entry) await recoverConflict(entry, cause.body.cart);
        replay.current = null; setUncertain(false); setError(t.stale);
      } else if (cause instanceof ApiError && (cause.status === 403 || cause.status === 410)) {
        if (entry) invalidateGrant(entry.grant.session.id);
      }
      else setError(t.network);
    } finally { inFlight.current = false; setBusy(false); }
  };

  const menuProducts = useMemo(() => {
    const products = new Map<string, GuestMenu['categories'][number]['products'][number]>();
    for (const category of entry?.menu.categories ?? []) for (const product of category.products) products.set(product.key, product);
    return products;
  }, [entry]);

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

  const selectionsValid = (productKey: string, selected: OptionSelection[]) => {
    const product = menuProducts.get(productKey);
    return !!product?.selectable && product.option_groups.every((group) => {
      const count = selected.filter((item) => item.group_key === group.key).length;
      return count >= group.min_select && count <= group.max_select;
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
    setBusy(true); setError(''); setNotice('');
    const key = newOperationKey();
    if (payment === 'STRIPE') {
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
      const result = await cartClient.commit(entry.grant.session.id, entry.grant.table_join_token, preview, payment, key);
      if (result.order_read_grant) rememberOrderGrant(result.order_read_grant);
      adoptCart(result.cart); setOrders((current) => [result.order, ...current.filter((order) => order.order_id !== result.order.order_id)]);
      selectCheckoutOrder(payment === 'STRIPE' ? result.order.order_id : '');
      clearPreview(); setTab('orders'); setNotice(result.outcome === 'ALREADY_COMMITTED' ? t.already : t.order);
      await refresh(entry);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        await recoverConflict(entry, cause.body.cart);
        clearPreview(); setError(cause.body.error === 'unavailable' ? t.unavailableLines : t.stale);
      } else if (cause instanceof ApiError && cause.status >= 500) {
        setError(t.network); setUncertain(true);
        replay.current = createReplayOperation(key, { preview, payment }, (original) => cartClient.commit(entry.grant.session.id, entry.grant.table_join_token, original.preview, original.payment, key));
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
        replay.current = createReplayOperation(key, { preview, payment }, (original) => cartClient.commit(entry.grant.session.id, entry.grant.table_join_token, original.preview, original.payment, key));
      }
    } finally { setBusy(false); }
  };

  if (loading) return <Shell demo={demo} locale={locale} setLocale={setLocale} t={t}><div className="loading-card" role="status">{t.loading}</div></Shell>;
  const visibleError = error === 'session_expired' ? t.closed : error;
  if (!entry || blocked) return <Shell demo={demo} locale={locale} setLocale={setLocale} t={t}>
    <section className="entry-card" aria-labelledby="entry-title">
      <div className="eyebrow">{t.subtitle}</div><h1 id="entry-title">{t.join}</h1><p>{t.joinHint}</p>
      <div className="table-chip"><span>{t.table}</span><strong>{query.locator ? displayTableLocator(query.locator, demo) : query.sessionID ?? '—'}</strong></div>
      {status?.expired && <p className="notice">{t.closed}</p>}
      {(!query.locator || status?.mode === 'SESSION_PIN') && <label className="field">{t.pin}<input autoComplete="one-time-code" inputMode="numeric" value={pin} onChange={(event) => setPin(event.target.value)} /></label>}
      {(!query.locator && !query.sessionID) && <p className="notice">テーブル QR からアクセスしてください。</p>}
      <button className="button primary wide" disabled={busy || blocked || (!query.locator && !query.sessionID) || !!status?.expired} onClick={() => void submitJoin()}>{busy ? t.loading : t.enter}</button>
      {visibleError && <p className="error" role="alert">{visibleError}</p>}
    </section>
  </Shell>;

  const lineCount = cart?.lines.reduce((sum, line) => sum + line.quantity, 0) ?? 0;
  const previewMatchesCart = !!preview && !!cart && preview.generation === cart.generation && preview.version === cart.version;
  const unavailable = cart?.lines.some((line) => !line.product_available || line.options.some((option) => !option.available) || !menuProducts.get(line.product_key)?.selectable || menuProducts.get(line.product_key)?.company_emergency_hold_active) ?? false;
  const previewAmountsSafe = !!preview && isSafeAmount(preview.quote.tax.total_gross_jpy) && isSafeAmount(preview.quote.tax.total_tax_jpy) && preview.quote.lines.every((line) => isSafeAmount(line.base_gross_jpy) && line.options.every((option) => isSafeAmount(option.price_gross_jpy)));

  return <Shell demo={demo} locale={locale} setLocale={setLocale} t={t}>
    <div className="workspace">
      <header className="welcome">
        <div><div className="eyebrow">{t.menu}</div><h1>{entry.locator ? `${t.table} ${displayTableLocator(entry.locator, demo)}` : t.brand}</h1><p>{mode === 'DIRECT_SCAN' ? t.directMode : mode === 'SESSION_PIN' ? t.pinMode : mode === 'STAFF_OPEN' ? t.staffMode : t.tableActive}</p></div>
        <div className="join-badge"><span className="live-dot" />{t.latest}</div>
      </header>
      <nav className="tabs" aria-label={locale === 'ja' ? '注文メニュー' : '點餐導覽'}>
        <button className={tab === 'cart' ? 'tab active' : 'tab'} onClick={() => { setTab('cart'); clearPreview(); }}>{t.pending}<span>{lineCount}</span></button>
        <button className={tab === 'orders' ? 'tab active' : 'tab'} onClick={() => { setTab('orders'); clearPreview(); }}>{t.committed}<span>{orders.length}</span></button>
      </nav>

      {tab === 'cart' && <>
        {!preview && <section className="menu-section"><div className="section-heading"><div><div className="eyebrow">TOJIMA KITCHEN</div><h2>{t.menu}</h2></div><span className="menu-count">{entry.menu.categories.reduce((n, c) => n + c.products.length, 0)}</span></div>
          {entry.menu.categories.map((category) => <section className="category" key={category.key}>
            <h3>{category.name}</h3><div className="product-grid">
              {category.products.map((product) => {
                const selected = selections[product.key] ?? [];
                const amountSafe = isSafeAmount(product.effective_price_minor) && product.option_groups.every((group) => group.options.every((option) => isSafeAmount(option.price_delta_minor)));
                const held = product.company_emergency_hold_active === true;
                const allowed = product.selectable && !held && amountSafe && selectionsValid(product.key, selected);
                return <article className="product-card" key={product.key}>
                  <div className="product-top"><div><h4>{product.name}</h4>{product.description && <p>{product.description}</p>}</div><strong className="price">{safeAmount(product.effective_price_minor)}</strong></div>
                  <span className={`availability ${product.selectable && !held ? 'is-available' : ''}`}>{held ? t.emergencyHold : product.selectable ? t.available : product.availability === 'PENDING_CONFIRMATION' ? t.pendingAvailability : t.unavailable}</span>
                  {product.option_groups.map((group) => <fieldset className="option-group" key={group.key}>
                    <legend>{group.name} {group.min_select > 0 && <small>{t.required}</small>}</legend>
                    <div className="option-list">{group.options.map((option) => {
                      const checked = selected.some((item) => item.group_key === group.key && item.option_key === option.key);
                      return <label className={`option ${!option.availability || option.availability !== 'AVAILABLE' ? 'option-muted' : ''}`} key={option.key}>
                        <input type={group.max_select === 1 ? 'radio' : 'checkbox'} name={`${product.key}-${group.key}`} checked={checked} disabled={!product.selectable || held || option.availability !== 'AVAILABLE' || (group.max_select !== 1 && !checked && selected.filter((item) => item.group_key === group.key).length >= group.max_select)} onChange={() => chooseOption(product.key, group.key, option.key, group.max_select)} />
                        <span>{option.name}</span><small>{safeAmount(option.price_delta_minor)}</small>
                      </label>;
                    })}</div>
                    {group.min_select === 0 && group.max_select === 1 && <button type="button" className="text-button option-clear" onClick={() => setSelections((current) => ({ ...current, [product.key]: (current[product.key] ?? []).filter((item) => item.group_key !== group.key) }))}>{t.noOption}</button>}
                  </fieldset>)}
                  <div className="product-actions"><label className="quantity-select">{t.quantity}<select aria-label={`${product.name} ${t.quantity}`} value={quantities[product.key] ?? 1} onChange={(event) => setQuantities((values) => ({ ...values, [product.key]: Number(event.target.value) }))}>{Array.from({ length: 10 }, (_, i) => i + 1).map((value) => <option key={value} value={value}>{value}</option>)}</select></label>
                    <button className="button small primary" disabled={busy || !allowed || uncertain} onClick={() => void applyMutation((key) => cartClient.addLine(entry.grant.session.id, entry.grant.table_join_token, cart!, product.key, quantities[product.key] ?? 1, selected, key))}>{t.add}</button>
                  </div>
                  {product.selectable && !amountSafe && <small className="field-hint" role="alert">{t.amountError}</small>}
                  {product.selectable && amountSafe && !allowed && <small className="field-hint">{t.selectRequired}</small>}
                </article>;
              })}
            </div>
          </section>)}
        </section>}

        <aside className={`cart-panel ${preview ? 'preview-mode' : ''}`}>
          {preview ? <>
            <div className="eyebrow">{t.preview}</div><h2>{t.preview}</h2><p className="muted">{t.previewHint}</p>
            <div className="quote-lines">{preview.quote.lines.map((line) => <article className="quote-line" key={line.line_id}>
              <div><strong>{line.name}</strong><span>{line.options.map((option) => option.name).join(' · ')}</span><small>{safeAmount(line.base_gross_jpy)} × {line.quantity}</small></div>
              <strong>{safeAmount(line.base_gross_jpy)} × {line.quantity}</strong>
              {line.options.map((option) => <small className="quote-option" key={`${line.line_id}-${option.group_key}-${option.option_key}`}>＋ {option.name} · {safeAmount(option.price_gross_jpy)} × {line.quantity}</small>)}
            </article>)}</div>
            <div className="total-row"><span>{t.total}</span><strong>{safeAmount(preview.quote.tax.total_gross_jpy)}</strong></div>
            <div className="tax-row"><span>{t.tax}</span><span>{safeAmount(preview.quote.tax.total_tax_jpy)}</span></div>
            <fieldset className="payment-choice"><legend>{t.method}</legend>{!demo && <label><input type="radio" name="payment" checked={payment === 'STRIPE'} onChange={() => setPayment('STRIPE')} />{t.stripe}</label>}{guestOperations?.cash_enabled && <label><input type="radio" name="payment" checked={payment === 'CASH'} onChange={() => setPayment('CASH')} />{demo ? t.demoCash : t.cash}</label>}</fieldset>
            <p className="payment-note">{payment === 'STRIPE' ? t.stripeNote : demo ? t.demoCash : t.cashNote}</p>
            {!previewAmountsSafe && <p className="error" role="alert">{t.amountError}</p>}
            {previewMatchesCart && <button className="button primary wide" disabled={busy || unavailable || uncertain || !previewAmountsSafe} onClick={() => void confirmOrder()}>{busy ? t.working : t.confirm}</button>}
            <button className="button quiet wide" disabled={busy} onClick={clearPreview}>{t.cancelPreview}</button>
          </> : <>
            <div className="cart-panel-head"><div><div className="eyebrow">OPEN TABLE</div><h2>{t.pending}</h2></div><button className="icon-button" aria-label={t.refresh} title={t.refresh} disabled={busy} onClick={() => void refreshCurrent(entry)}>↻</button></div>
            {!cart || cart.lines.length === 0 ? <div className="empty-state"><div className="empty-icon">＋</div><p>{t.cartEmpty}</p></div> : <div className="cart-lines">
              {cart.lines.map((line: CartLine) => {
                const product = menuProducts.get(line.product_key);
                if (!product) return <article className="cart-line unavailable-line" key={line.line_id}><div><strong>{line.product_name || line.product_key}</strong><p>{line.unavailable_code === 'COMPANY_EMERGENCY_HOLD' ? t.emergencyHold : t.itemMissing}</p><p>{line.options.map((option) => option.option_name).join(' · ')}</p></div><button className="text-button" disabled={busy || uncertain} onClick={() => void applyMutation((key) => cartClient.removeLine(entry.grant.session.id, entry.grant.table_join_token, cart, line.line_id, key))}>{t.remove}</button></article>;
                const selected = line.options;
                const held = line.unavailable_code === 'COMPANY_EMERGENCY_HOLD' || product.company_emergency_hold_active === true;
                const lineAvailable = line.product_available && product.selectable && !held && line.options.every((option) => option.available);
                return <article className="cart-line" key={line.line_id}>
                  <div className="cart-line-head"><div><strong>{line.product_name || product.name}</strong><p>{held ? t.emergencyHold : lineAvailable ? t.available : t.unavailable}</p>{selected.length > 0 && <p className="cart-selected-options">{selected.map((option) => `${option.group_name}: ${option.option_name}${option.available ? '' : ` · ${t.unavailable}`}`).join('、')}</p>}</div><button className="text-button danger" disabled={busy || uncertain} onClick={() => void applyMutation((key) => cartClient.removeLine(entry.grant.session.id, entry.grant.table_join_token, cart, line.line_id, key))}>{t.remove}</button></div>
                  {product.option_groups.map((group) => <fieldset className="compact-options" key={group.key}><legend>{group.name}</legend>{group.options.map((option) => <label key={option.key}><input type={group.max_select === 1 ? 'radio' : 'checkbox'} name={`${line.line_id}-${group.key}`} checked={selected.some((item) => item.group_key === group.key && item.option_key === option.key)} disabled={busy || uncertain || !product.selectable || held || option.availability !== 'AVAILABLE'} onChange={() => {
                    const exists = selected.some((item) => item.group_key === group.key && item.option_key === option.key);
                    const groupSelections = selected.filter((item) => item.group_key !== group.key);
                    const sameGroup = selected.filter((item) => item.group_key === group.key);
                    const next = exists ? selected.filter((item) => !(item.group_key === group.key && item.option_key === option.key)) : [...groupSelections, ...sameGroup.slice(0, Math.max(0, group.max_select - 1)), { group_key: group.key, option_key: option.key }];
                    const count = next.filter((item) => item.group_key === group.key).length;
                    if (count >= group.min_select && count <= group.max_select) void applyMutation((key) => cartClient.setLine(entry.grant.session.id, entry.grant.table_join_token, cart, line.line_id, line.quantity, next, key));
                  }} />{option.name}</label>)}{group.min_select === 0 && group.max_select === 1 && <button type="button" className="text-button option-clear" disabled={busy || uncertain} onClick={() => {
                    const next = selected.filter((item) => item.group_key !== group.key);
                    void applyMutation((key) => cartClient.setLine(entry.grant.session.id, entry.grant.table_join_token, cart, line.line_id, line.quantity, next, key));
                  }}>{t.noOption}</button>}</fieldset>)}
                  <div className="cart-line-foot"><span>{amountJPY(product.effective_price_minor)}</span><div className="stepper"><button aria-label={`${product.name} −`} disabled={busy || uncertain || line.quantity <= 1} onClick={() => void applyMutation((key) => cartClient.setLine(entry.grant.session.id, entry.grant.table_join_token, cart, line.line_id, line.quantity - 1, line.options, key))}>−</button><span>{line.quantity}</span><button aria-label={`${product.name} +`} disabled={busy || uncertain || line.quantity >= 99} onClick={() => void applyMutation((key) => cartClient.setLine(entry.grant.session.id, entry.grant.table_join_token, cart, line.line_id, line.quantity + 1, line.options, key))}>＋</button></div></div>
                </article>;
              })}
            </div>}
            {unavailable && <p className="notice">{t.unavailableLines}</p>}
            <button className="button primary wide checkout-button" disabled={!cart?.lines.length || busy || uncertain || unavailable} onClick={() => void openPreview()}>{busy ? t.loading : t.preview}</button>
            <p className="sync-caption"><span className="live-dot" />{locale === 'ja' ? '同じテーブルと共有中' : '與同桌即時共用'}</p>
          </>}
          {uncertain && <button className="button retry wide" disabled={busy} onClick={() => void retryUncertain()}>{t.retry}</button>}
          {visibleError && <p className="error" role="alert">{visibleError}</p>}{notice && <p className="notice" role="status">{notice}</p>}
        </aside>
      </>}

      {tab === 'orders' && <section className="orders-panel"><div className="section-heading"><div><div className="eyebrow">CONFIRMED</div><h2>{t.committed}</h2></div><button className="icon-button" aria-label={t.refresh} onClick={() => void refreshCurrent(entry)}>↻</button></div>
        {operationsStale && <p className="notice" role="status">{guestOperations ? t.operationsStale : t.amountUnavailable}</p>}
        {orders.length === 0 ? <div className="empty-state"><div className="empty-icon">✓</div><p>{t.noOrders}</p></div> : orders.map((order) => { const operations = guestOperations?.orders.find((item) => item.id === order.order_id); return <article className="order-card" key={order.order_id}>
          <RequestOrderLink sessionID={entry.grant.session.id} token={entry.grant.table_join_token} orderID={order.order_id} locale={locale} />
          <div className="order-card-title"><div><span className="order-kicker">{order.requires_staff_acceptance ? t.staffWaiting : t.acceptedOrder} · #{order.generation}</span><h3>{order.order_id}</h3></div><span className="order-method">{order.payment_method === 'STRIPE' ? t.stripe : demo ? t.demoCash : t.cash}</span></div>
          <p className="guest-order-status" role="status">{guestOrderLabel(operations, locale)}</p>
          {order.payment_method === 'STRIPE' && !operationsStale && operations && guestDisplayKind(operations) === 'standard' && <div className="checkout-status-panel">
            <strong>{t.checkoutStatus}</strong>
            {checkoutOrderID === order.order_id && <>
              {checkoutStatus?.order_id === order.order_id && <p className="guest-order-status">{checkoutStateLabel(checkoutStatus.state, locale)}</p>}
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
      <footer className="workspace-footer">{t.brand} <span>·</span> {locale === 'ja' ? 'おいしい時間を、みんなで。' : '與同桌一起享用好味道。'}</footer>
    </div>
  </Shell>;
}

function Shell({ children, demo = false, locale, setLocale, t }: { children: React.ReactNode; demo?: boolean; locale: Locale; setLocale: (locale: Locale) => void; t: Record<string, string> }) {
  return <main className="app-shell"><meta name="referrer" content="no-referrer" /><header className="topbar"><a className="brand" href="/" aria-label="Tojima"><span className="brand-mark">東</span><span><strong>{t.brand}</strong><small>{t.subtitle}</small></span></a><button className="locale-switch" onClick={() => setLocale(locale === 'ja' ? 'zh' : 'ja')}>{t.locale} <span>⌄</span></button></header><DemoBanner demo={demo} locale={locale} />{children}<MyOrders locale={locale} /></main>;
}
