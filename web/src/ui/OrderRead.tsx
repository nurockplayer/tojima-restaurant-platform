import { useEffect, useRef, useState } from 'react';
import { QueryClient, useQuery } from '@tanstack/react-query';
import { amountJPY } from '../api/client';
import { grantFromFragment, orderReadClient, orderReadLink, type OrderReadGrant, type OrderReadStatus } from '../api/order-read';
import { loadMyOrders, myOrdersChanged, rememberOrderGrant } from './my-orders';
import styles from './OrderRead.module.css';
import { guestDisplayKind, guestRefundKind, paidUnderReview } from './guest-display';
import { DemoBanner, type DemoSurfaceProps } from './DemoBanner';

type Locale = 'ja' | 'zh';
const ja = {
  title: '注文状況', mine: 'マイ注文', empty: 'このブラウザに保存された注文はありません。', loading: '注文状況を確認しています…',
  unavailable: '注文状況を確認できません。通信状態をご確認ください。', amountUnavailable: '最新の注文内容と金額を確認できません。', stale: '最新の状況を確認できません。表示は前回確認時の内容です。', payment: 'お支払い', kitchen: '調理・提供', total: '税込合計', original: '注文確定時の税込合計', changed: '店舗により注文内容が変更されました', cancelled: 'この注文は取り消されました', refundPending: '返金処理中', refundFailed: '返金を確認中です。店舗にお問い合わせください', refunded: '返金済み', refundedTotal: '返金合計',
  unknown: '店舗に確認してください', copy: '共有リンクをコピー', copied: 'コピーしました', link: '共有リンク', expires: '閲覧期限',
  privacy: 'リンクを知っている人はこの注文を閲覧できます。期限付きのリンクです。', saved: 'このブラウザに保存した期限付きリンクです。', invalid: '閲覧リンクを確認してください。',
  request: 'この注文の閲覧リンクを保存', failed: 'リンクを取得できません。現在のテーブルへの参加状況を確認してください。',
};
const copy: Record<Locale, Record<keyof typeof ja, string>> = { ja, zh: {
  title: '訂單狀態', mine: '我的訂單', empty: '此瀏覽器尚未儲存訂單。', loading: '正在確認訂單狀態…',
  unavailable: '無法確認訂單狀態，請確認網路連線。', amountUnavailable: '目前無法確認最新訂單內容與金額。', stale: '無法確認最新狀態，目前顯示上次確認的內容。', payment: '付款', kitchen: '製作與送餐', total: '含稅總額', original: '送單時含稅總額', changed: '店家已調整您的訂單', cancelled: '此訂單已取消', refundPending: '退款處理中', refundFailed: '退款確認中，請洽店家', refunded: '已退款', refundedTotal: '退款總額',
  unknown: '請洽服務人員確認', copy: '複製分享連結', copied: '已複製', link: '分享連結', expires: '查閱期限',
  privacy: '持有連結的人可以查閱此筆訂單。連結有使用期限。', saved: '這些是儲存在此瀏覽器的限時連結。', invalid: '請確認查閱連結。',
  request: '儲存此筆訂單的查閱連結', failed: '無法取得連結，請確認是否仍加入目前桌次。',
} };
const paymentLabels: Record<Locale, Record<string, string>> = {
  ja: { UNPAID: '未払い', PENDING: '確認中', PAID: '支払済み', UNKNOWN: '確認中', REFUNDED: '返金済み', REFUND_PENDING: '返金処理中' },
  zh: { UNPAID: '尚未付款', PENDING: '確認中', PAID: '已付款', UNKNOWN: '確認中', REFUNDED: '已退款', REFUND_PENDING: '退款處理中' },
};
const kitchenLabels: Record<Locale, Record<string, string>> = {
  ja: { WAITING: '受付待ち', ACCEPTED: '受付済み', PREPARING: '調理中', READY: '配膳待ち', SERVED: '提供済み', CANCELLED: '取消済み' },
  zh: { WAITING: '等待接單', ACCEPTED: '已接單', PREPARING: '製作中', READY: '待送餐', SERVED: '已送達', CANCELLED: '已取消' },
};
export function orderPollInterval(value = 30_000): number {
  return Number.isFinite(value) ? Math.min(30_000, Math.max(15_000, value)) : 30_000;
}

type OrderStatusProps = { grant: OrderReadGrant; locale: Locale; pollMs?: number; onRead?: (grant: OrderReadGrant) => void };
export function OrderStatusView(props: OrderStatusProps) {
  // A new capability gets a fresh client and no previous capability's cache.
  return <Status key={`${props.grant.order_id}:${props.grant.order_read_token}`} {...props} />;
}
function Status({ grant, locale, pollMs, onRead }: OrderStatusProps) {
  const t = copy[locale];
  const [client] = useState(() => new QueryClient());
  const [visible, setVisible] = useState(document.visibilityState === 'visible');
  const [shared, setShared] = useState(false);
  const [copied, setCopied] = useState(false);
  const confirmed = useRef(false);
  // Track the underlying request too: even a transport that settles late after
  // abort cannot overlap a resumed request or replace the new query's result.
  const flight = useRef<Promise<OrderReadStatus> | null>(null);
  const query = useQuery({
    queryKey: ['order-status', grant.order_id, grant.order_read_token],
    queryFn: async ({ signal }) => {
      if (flight.current) await flight.current.catch(() => undefined);
      signal.throwIfAborted();
      const pending = orderReadClient.status(grant, signal);
      flight.current = pending;
      try { const result = await pending; signal.throwIfAborted(); return result; }
      finally { if (flight.current === pending) flight.current = null; }
    },
    enabled: visible,
    retry: false, retryOnMount: false, refetchOnWindowFocus: false, refetchOnReconnect: false,
    refetchOnMount: true, staleTime: 0, gcTime: 0, networkMode: 'always',
    refetchInterval: (q) => visible && !q.state.data?.is_terminal ? orderPollInterval(pollMs) : false,
    refetchIntervalInBackground: false,
  }, client);
  useEffect(() => {
    // Save a fragment only after the server has accepted this capability, once
    // per view so later polling cannot overwrite a newer saved link.
    if (query.isSuccess && !confirmed.current) {
      confirmed.current = true;
      onRead?.(grant);
    }
  }, [query.isSuccess, grant, onRead]);
  useEffect(() => {
    const changed = () => {
      const next = document.visibilityState === 'visible';
      if (!next) void client.cancelQueries();
      setVisible(next);
    };
    document.addEventListener('visibilitychange', changed);
    return () => { document.removeEventListener('visibilitychange', changed); client.clear(); };
  }, [client]);
  const share = async () => {
    setShared(true); setCopied(false);
    try { await navigator.clipboard.writeText(orderReadLink(grant)); setCopied(true); } catch { /* Show selectable link below. */ }
  };
  const display = query.data ? guestDisplayKind(query.data) : 'unknown';
  return <section className={styles.panel} aria-label={t.title}>
    <h1>{t.title}</h1><p>{grant.order_id}</p>
    {query.isError && <p role="status">{query.data ? t.stale : t.unavailable}</p>}
    {!query.data && !query.isError && <p role="status">{t.loading}</p>}
    {query.data && <>
      {display !== 'standard' && <p role="status">{display === 'review' ? paidUnderReview[locale] : display === 'cancelled' ? t.cancelled : t.unknown}</p>}
      <dl>
        {display === 'standard' && <>
          <dt>{t.payment}</dt><dd>{paymentLabels[locale][query.data.payment_status] ?? t.unknown}</dd>
          <dt>{t.kitchen}</dt><dd>{kitchenLabels[locale][query.data.fulfillment_status] ?? t.unknown}</dd>
        </>}
        <dt>{t.total}</dt><dd>{typeof query.data.effective_total_gross_jpy === 'string' && /^\d+$/.test(query.data.effective_total_gross_jpy) ? amountJPY(query.data.effective_total_gross_jpy) : t.amountUnavailable}</dd>
        {query.data.adjusted && <><dt>{t.original}</dt><dd>{amountJPY(query.data.total_gross_jpy)}</dd></>}
      </dl>
      {query.data.adjusted && <p role="status">{t.changed}</p>}
      {(query.data.effective_lines ?? []).length > 0 && <ul aria-label={t.total}>{query.data.effective_lines.map((line, index) => <li key={`${line.product_name}-${index}`}><span>{line.product_name} × {line.quantity}{(line.option_names ?? []).map((name, optionIndex) => <small className="order-option" key={`${optionIndex}-${name}`}>{name}</small>)}</span> <strong>{amountJPY(line.line_total_jpy)}</strong></li>)}</ul>}
      {(query.data.refunds ?? []).map((refund, index) => { const kind = guestRefundKind(refund); return <p role="status" key={`${refund.status}-${index}`}>{kind === 'processing' ? t.refundPending : kind === 'succeeded' ? `${t.refunded} ${amountJPY(refund.amount_jpy)}` : t.refundFailed}</p>; })}
      {/^\d+$/.test(query.data.refunded_total_jpy ?? '') && BigInt(query.data.refunded_total_jpy) > 0n && <p>{t.refundedTotal}: {amountJPY(query.data.refunded_total_jpy)}</p>}
    </>}
    <p>{t.expires}: {new Date(grant.expires_at).toLocaleString(locale === 'ja' ? 'ja-JP' : 'zh-TW', { timeZone: 'Asia/Tokyo', timeZoneName: 'short' })}</p>
    <p>{t.privacy}</p><button type="button" onClick={() => void share()}>{copied ? t.copied : t.copy}</button>
    {shared && <label>{t.link}<input readOnly value={orderReadLink(grant)} onFocus={(e) => e.currentTarget.select()} /></label>}
  </section>;
}

export function MyOrders({ locale }: { locale: Locale }) {
  const t = copy[locale];
  const [orders, setOrders] = useState(loadMyOrders);
  useEffect(() => {
    const refresh = () => setOrders(loadMyOrders());
    window.addEventListener(myOrdersChanged, refresh);
    window.addEventListener('storage', refresh);
    return () => { window.removeEventListener(myOrdersChanged, refresh); window.removeEventListener('storage', refresh); };
  }, []);
  return <aside className={styles.panel} aria-label={t.mine}><h2>{t.mine}</h2><p>{t.saved}</p>
    {orders.length === 0 ? <p>{t.empty}</p> : <ul>{orders.map((g) => <li key={g.order_id}><a href={orderReadLink(g)} referrerPolicy="no-referrer">{g.order_id}</a></li>)}</ul>}
  </aside>;
}
export function RequestOrderLink({ sessionID, token, orderID, locale }: { sessionID: string; token: string; orderID: string; locale: Locale }) {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const [grant, setGrant] = useState<OrderReadGrant | null>(null);
  const t = copy[locale];
  const issue = async () => {
    if (busy) return;
    setBusy(true); setFailed(false);
    try { const g = await orderReadClient.issue(sessionID, orderID, token); rememberOrderGrant(g); setGrant(g); }
    catch { setFailed(true); }
    finally { setBusy(false); }
  };
  return <div><button type="button" disabled={busy} onClick={() => void issue()}>{t.request}</button>
    {failed && <p role="alert">{t.failed}</p>}{grant && <a href={orderReadLink(grant)} referrerPolicy="no-referrer">{t.title}</a>}
  </div>;
}
export function OrderPage({ demo = false }: DemoSurfaceProps) {
  const [locale, setLocale] = useState<Locale>('ja');
  const [grant, setGrant] = useState(() => grantFromFragment(window.location.hash));
  useEffect(() => {
    const changed = () => setGrant(grantFromFragment(window.location.hash));
    window.addEventListener('hashchange', changed);
    return () => window.removeEventListener('hashchange', changed);
  }, []);
  useEffect(() => { document.documentElement.lang = locale === 'ja' ? 'ja' : 'zh-Hant'; }, [locale]);
  return <main><meta name="referrer" content="no-referrer" /><button type="button" onClick={() => setLocale(locale === 'ja' ? 'zh' : 'ja')}>{locale === 'ja' ? '繁體中文' : '日本語'}</button>
    <DemoBanner demo={demo} locale={locale} />
    {grant ? <OrderStatusView grant={grant} locale={locale} onRead={rememberOrderGrant} /> : <p role="alert">{copy[locale].invalid}</p>}
    <MyOrders locale={locale} />
  </main>;
}
