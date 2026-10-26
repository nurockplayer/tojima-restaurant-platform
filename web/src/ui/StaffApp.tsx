import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, amountJPY, newOperationKey } from '../api/client';
import { operationsClient, setCookieSessionUnauthorizedHandler } from '../api/operations';
import type { AdjustmentPreview, AdjustmentRequest, Capabilities, CashPreview, CashSettings, OpenedSession, Receipt, SessionOperations, StaffContext, StaffOrder, StaffTable } from '../api/operations';
import { probeStaffAuth, staffLoginURL, staffStepUpURL, submitStaffLogout } from '../api/staff-auth';
import type { StaffAuthState } from '../api/staff-auth';
import { checkoutClient } from '../api/checkout';
import type { CheckoutState, CheckoutStatus } from '../api/checkout';
import { createReplayOperation } from './cart-state';
import { StaffDialog } from './StaffDialog';
import { DemoBanner } from './DemoBanner';
import type { DemoSurfaceProps } from './DemoBanner';

type Locale = 'ja' | 'zh';
type View = 'tables' | 'kds' | 'reviews';
type Dialog = 'cash' | 'close' | 'approve' | 'adjust' | 'stop' | 'void' | null;
const CONTEXT_KEY = 'tojima.staff.context';
const TOKEN_KEY = 'tojima.staff.access-token';
// The HTTP client has no timeout, so a hung KDS poll is aborted (and the board marked stale) after this.
const KDS_POLL_TIMEOUT_MS = 10000;

const copy = {
  ja: {
    brand: '東島', subtitle: 'STAFF OPERATIONS', tenant: '企業ID', store: '店舗ID', token: 'スタッフアクセストークン', connect: '接続', disconnect: '切断', login: 'ログイン', logout: 'ログアウト', loginIntro: '店舗アカウントでログインしてください。', stepUpNotice: 'この操作には再認証が必要です。', stepUp: '再認証',
    tables: 'テーブル', kds: '厨房看板', refresh: '最新状態に更新', loading: '確認中…', noAccess: 'この店舗で利用できる操作がありません。',
    idle: '空席', open: '利用中', closed: '終了', openTable: '開卓', enter: '詳細', unpaid: '未収現金', count: '注文',
    noTables: 'テーブルがありません。', detail: 'テーブル詳細', back: '一覧に戻る', orders: '注文', receipts: '現金領収', noOrders: '注文はありません。',
    accepted: '受付済み', preparing: '調理中', ready: '配膳待ち', served: '提供済み', waiting: '受付確認中', cancelled: '取消済み',
    paid: '支払済み', refunded: '返金済み', refundPending: '返金処理中', unpaidStatus: '未収', pending: '処理中', unknown: '確認中', stripe: 'クレジットカード', cash: '現金',
    receive: '注文を受け付ける', start: '調理開始', markReady: '調理完了', markServed: '提供済みにする',
    cashSettle: '現金を精算', cashPreview: '未収現金を確認', cashTitle: '現金精算', due: '今回の現金合計', tax: '消費税額', tendered: 'お預かり金額', registerRef: 'レジ精算番号', settle: '現金を確認して精算',
    receipt: '精算済み', receiptID: '領収番号', change: 'お釣り', cashier: '担当者', close: 'テーブルを閉じる', closeTitle: 'テーブルを閉じますか？', closeConfirm: '閉卓を確認', blockers: '閉卓できない注文があります。', noBlockers: '未収・処理中の支払いや未提供の注文はありません。',
    cashSettings: '現金受付設定', enabled: '現金を受け付ける', prepaid: '先払い', payLater: '後払い', save: '設定を保存',
    noCashOrders: '精算対象の現金注文はありません。', closeBlocked: '未提供または未精算の注文があるため閉卓できません。',
    reviews: '要確認', reviewQueue: '支払済み・要確認の注文', noReviews: '確認が必要な注文はありません。', approve: '提供を承認', approveTitle: '提供を承認しますか？', reason: '承認理由', confirmApprove: '承認して厨房へ送る', reviewCompany: '企業の商品保留', reviewOperations: '運営保留', reviewClosed: '卓セッション終了後の確認', reviewPayment: '支払い確認', reviewRefund: '返金未完了', effectiveTotal: '変更後合計', originalTotal: '元の合計', reviewStale: '注文が更新されました。最新状態を読み込みました。内容を確認してください。', reviewLoading: '確認が必要な注文を読み込んでいます…', reviewDataStale: '表示中の確認一覧が最新ではありません。再読み込みしてください。', reviewLoadError: '確認一覧を読み込めませんでした。', companyHoldActive: '企業の商品保留中です。解除後に再度お試しください。', reviewNotReady: 'この注文は承認できません。最新状態を確認してください。',
    adjust: '取消・減量', adjustTitle: '注文内容の取消・数量変更', adjustReason: '取消理由（必須）', previewAdjustment: '変更内容を確認', adjustedTotal: '変更後合計', refundAmount: '返金額', confirmAdjustment: '変更を確定', cashHandback: '現金をお客様へ返金しました（手渡し確認）', refunds: '返金状況', refundProcessing: '処理中', refundDispatchPending: '返金の送信待ち（自動再試行）', refundReconciliationNeeded: '返金状況の確認が必要です', refundFailed: '失敗', refundSucceeded: '完了', retryRefund: '返金を再試行', orderUpdated: '注文が更新されました。厨房で内容を確認してください。', acknowledgeUpdate: '更新を確認',
    voidUnpaid: '未払いとして取消', voidTitle: '未払いとして注文を取り消しますか？', voidExplanation: 'Stripe に支払状況を再確認し、未完了の決済画面を閉じてから注文を取り消します。お客様には請求されません。', voidReason: '取消理由（必須）', confirmVoid: '取消を確定', voidIneligible: '支払いが完了しているか処理中のため取り消せません。照合結果を確認してください。', voidUnavailable: 'Stripe に接続できません。時間をおいて再試行してください。', voided: '未払い取消',
    stopRequest: '調理中の商品を止める', stopTitle: '停止を依頼', stopNoRemaining: '停止できる数量がありません', stopUnavailable: '最新状態を確認してください', stopPending: '停止依頼中', stopConfirmed: '厨房が停止を確認しました', stopFinalized: '取消を確定', stopWithdraw: '依頼を取り下げる', stopKitchenConfirm: '停止を確認', stopKitchenReject: '停止できない', stopReason: '停止理由（必須）', nonDelivery: '商品が提供されていないことを確認しました', uncertainDelivery: '提供状況が不明なため手動確認が必要です', stopRequestPending: '停止依頼を確認してください', stopManual: '手動対応が必要です', stopRejected: '厨房で停止できませんでした', stopWithdrawn: '停止依頼を取り下げました', stopConfirmTitle: '停止済みであることを確認しますか？', stopWithdrawTitle: '停止依頼を取り下げますか？', stopRejectTitle: '停止できない理由を記録しますか？', stopRequestTitle: 'この商品を止めるよう厨房へ依頼しますか？',
    paymentCheck: '支払い状況を確認', paymentReconcile: '最新の支払い状況を確認', paymentError: '支払い状況を確認できません。時間をおいてお試しください。', paymentReview: '支払い状況を店舗で確認する必要があります。', lastPaymentCheck: '最終確認',
    kdsStale: '厨房看板の接続が切れています。表示が最新ではない可能性があります。自動で再接続しています。', kdsLastSync: '最終同期',
    stateCreatePending: '注文を確認しています', stateCreating: 'お支払い画面を準備しています', stateOpen: 'お支払い手続きができます', stateProcessing: 'お支払いを確認しています', stateUnknown: 'お支払い状況を確認しています', statePaid: 'お支払い済み', stateExpired: 'お支払い期限が切れました', stateFailed: 'お支払いを完了できませんでした', stateReview: 'お支払い状況を店舗で確認しています',
    retry: '同じ操作を確認して再試行', network: '処理結果を確認できません。再試行は同じ操作を照合します。', stale: '最新情報を読み直しました。内容を確認してから操作してください。',
    error: '処理できませんでした。入力内容と権限を確認してください。', accessError: 'スタッフ権限を確認できません。企業・店舗IDとアクセストークンを確認してください。', authUnavailable: 'ログイン状態を確認できません。しばらくしてから再試行してください。', retryAuth: '再試行',
    ja: '日本語', zh: '繁體中文', mode: '現金方式', closeDialog: 'キャンセル', session: '卓セッション', updated: '同期済み', demoCash: '現金（デモ・テスト専用）', demoCashTitle: '現金精算（デモ・テスト専用）', demoReceipt: 'テスト用領収書 · 実際の入金ではありません', demoClose: 'デモ環境のテーブルを閉じる',
  },
  zh: {
    brand: '東島', subtitle: '店員作業台', tenant: '企業 ID', store: '分店 ID', token: '店員存取權杖', connect: '連線', disconnect: '登出', login: '登入', logout: '登出', loginIntro: '請使用店舗帳號登入。', stepUpNotice: '此操作需要重新驗證。', stepUp: '重新驗證',
    tables: '桌次', kds: '廚房看板', refresh: '更新最新狀態', loading: '確認中…', noAccess: '此分店目前沒有可用的操作權限。',
    idle: '空桌', open: '用餐中', closed: '已結束', openTable: '開桌', enter: '查看', unpaid: '待收現金', count: '筆訂單',
    noTables: '目前沒有桌次。', detail: '桌次內容', back: '返回桌次', orders: '訂單', receipts: '現金收據', noOrders: '目前沒有訂單。',
    accepted: '待製作', preparing: '製作中', ready: '待送餐', served: '已送達', waiting: '待店員確認', cancelled: '已取消',
    paid: '已付款', refunded: '已退款', refundPending: '退款處理中', unpaidStatus: '未收款', pending: '處理中', unknown: '確認中', stripe: '信用卡', cash: '現金',
    receive: '確認接單', start: '開始製作', markReady: '製作完成', markServed: '確認已送達',
    cashSettle: '現金結帳', cashPreview: '確認待收現金', cashTitle: '現金結帳', due: '本次應收現金', tax: '消費稅額', tendered: '實收金額', registerRef: '收銀交易編號', settle: '確認收款並結帳',
    receipt: '已完成結帳', receiptID: '收據編號', change: '找零', cashier: '收銀人員', close: '關閉桌次', closeTitle: '確定關閉此桌次？', closeConfirm: '確認關桌', blockers: '仍有訂單未完成，無法關桌。', noBlockers: '沒有未收款、處理中付款或未送達的訂單。',
    cashSettings: '現金收款設定', enabled: '接受現金付款', prepaid: '先付款', payLater: '後付款', save: '儲存設定',
    noCashOrders: '目前沒有待收現金訂單。', closeBlocked: '尚有未送達或未結清的訂單，無法關桌。',
    reviews: '待審核', reviewQueue: '已付款且待審核的訂單', noReviews: '目前沒有待審核的訂單。', approve: '核准出餐', approveTitle: '要核准此訂單出餐嗎？', reason: '核准原因', confirmApprove: '核准並送至廚房', reviewCompany: '企業商品保留', reviewOperations: '營運保留', reviewClosed: '桌次結束後確認', reviewPayment: '付款確認', reviewRefund: '退款尚未完成', effectiveTotal: '調整後總額', originalTotal: '原始總額', reviewStale: '訂單狀態已更新。已重新載入最新資料，請確認後操作。', reviewLoading: '正在載入待審核訂單…', reviewDataStale: '目前顯示的審核清單不是最新資料，請重新載入。', reviewLoadError: '無法載入審核清單。', companyHoldActive: '企業商品仍在保留中，解除後才能核准。', reviewNotReady: '此訂單目前無法核准，請確認最新狀態。',
    adjust: '取消・減量', adjustTitle: '取消或減少訂購數量', adjustReason: '取消原因（必填）', previewAdjustment: '預覽變更', adjustedTotal: '變更後總額', refundAmount: '退款金額', confirmAdjustment: '確認變更', cashHandback: '已將現金退還顧客（確認已交付）', refunds: '退款狀態', refundProcessing: '處理中', refundDispatchPending: '退款等待送出（自動重試）', refundReconciliationNeeded: '需要確認退款狀態', refundFailed: '失敗', refundSucceeded: '完成', retryRefund: '重試退款', orderUpdated: '訂單已更新，請廚房確認內容。', acknowledgeUpdate: '確認更新',
    voidUnpaid: '以未付款作廢', voidTitle: '要以未付款作廢此訂單嗎？', voidExplanation: '系統會向 Stripe 重新確認付款狀態，關閉未完成的付款頁面後取消此訂單，不會向顧客收費。', voidReason: '作廢原因（必填）', confirmVoid: '確認作廢', voidIneligible: '付款已完成或仍在處理中，無法作廢。請確認核對結果。', voidUnavailable: '無法連線 Stripe，請稍後再試。', voided: '未付款作廢',
    stopRequest: '申請停止製作中品項', stopTitle: '申請停止', stopNoRemaining: '沒有可停止的數量', stopUnavailable: '請確認最新狀態', stopPending: '停止申請待處理', stopConfirmed: '廚房已確認停止', stopFinalized: '確認取消', stopWithdraw: '撤回申請', stopKitchenConfirm: '確認已停止', stopKitchenReject: '無法停止', stopReason: '停止原因（必填）', nonDelivery: '我確認商品尚未送達', uncertainDelivery: '送達狀態不明，需人工處理', stopRequestPending: '請處理停止申請', stopManual: '需要人工處理', stopRejected: '廚房無法停止', stopWithdrawn: '已撤回停止申請', stopConfirmTitle: '確認廚房已停止製作？', stopWithdrawTitle: '要撤回停止申請嗎？', stopRejectTitle: '記錄無法停止的原因？', stopRequestTitle: '要請廚房停止此品項嗎？',
    paymentCheck: '確認付款狀態', paymentReconcile: '更新付款狀態', paymentError: '目前無法確認付款狀態，請稍後再試。', paymentReview: '需要由門店確認付款狀態。', lastPaymentCheck: '最後確認',
    kdsStale: '廚房看板連線中斷，畫面可能不是最新狀態，系統正在自動重新連線。', kdsLastSync: '最後同步',
    stateCreatePending: '正在確認訂單', stateCreating: '正在準備付款畫面', stateOpen: '可以繼續付款', stateProcessing: '正在確認付款', stateUnknown: '正在確認付款狀態', statePaid: '已付款', stateExpired: '付款期限已過', stateFailed: '付款未完成', stateReview: '門店正在確認付款狀態',
    retry: '以相同操作確認並重試', network: '目前無法確認操作結果。重試會比對同一筆操作。', stale: '已更新最新狀態，請確認內容後再操作。',
    error: '無法完成操作，請確認資料與權限。', accessError: '無法確認店員權限，請檢查企業／分店 ID 與存取權杖。', authUnavailable: '無法確認登入狀態，請稍後再試。', retryAuth: '重試',
    ja: '日本語', zh: '繁體中文', mode: '現金方式', closeDialog: '取消', session: '桌次工作階段', updated: '已同步', demoCash: '現金（展示測試專用）', demoCashTitle: '現金結帳（展示測試專用）', demoReceipt: '測試收據 · 非實際收款', demoClose: '關閉展示測試桌次',
  },
} as const;

interface PendingWrite { key: string; epoch: number; run: () => Promise<unknown>; success: (value: unknown) => Promise<void> | void; conflict: () => Promise<void>; unavailableCopy?: () => string }
interface ActiveStaffContext { epoch: number; context: StaffContext; bearer: string; ready: boolean }

export function StaffApp({ demo = false, initialView = 'tables', staffAuth = { kind: 'unsupported' } }: DemoSurfaceProps) {
  const [authState, setAuthState] = useState<StaffAuthState>(staffAuth);
  const cookieMode = !demo && authState.kind !== 'unsupported';
  const [authPrompt, setAuthPrompt] = useState<'login' | 'step-up' | null>(null);
  const [locale, setLocale] = useState<Locale>('ja');
  const t = copy[locale];
  const [context, setContext] = useState<StaffContext>({ tenantID: '', storeID: '' });
  const [token, setToken] = useState('');
  const [capabilities, setCapabilities] = useState<Capabilities | null>(null);
  const [connected, setConnected] = useState(false);
  const [view, setView] = useState<View>(initialView);
  const [tables, setTables] = useState<StaffTable[]>([]);
  const [kdsOrders, setKdsOrders] = useState<StaffOrder[]>([]);
  const [reviewOrders, setReviewOrders] = useState<StaffOrder[]>([]);
  const [reviewLoaded, setReviewLoaded] = useState(false);
  const [reviewLoading, setReviewLoading] = useState(false);
  const [reviewStale, setReviewStale] = useState(false);
  const [reviewError, setReviewError] = useState('');
  const [approveOrder, setApproveOrder] = useState<StaffOrder | null>(null);
  const [approveReason, setApproveReason] = useState('');
  const [voidOrder, setVoidOrder] = useState<StaffOrder | null>(null);
  const [voidReason, setVoidReason] = useState('');
  const voidKey = useRef('');
  const [adjustOrder, setAdjustOrder] = useState<StaffOrder | null>(null);
  const [stopOrder, setStopOrder] = useState<StaffOrder | null>(null);
  const [stopMode, setStopMode] = useState<'request' | 'reject' | 'finalize' | 'withdraw' | 'confirm' | null>(null);
  const [stopRequestID, setStopRequestID] = useState('');
  const [adjustReason, setAdjustReason] = useState('');
  const [cancelQty, setCancelQty] = useState<number[]>([]);
  const [adjustPreview, setAdjustPreview] = useState<AdjustmentPreview | null>(null);
  const [adjustPreviewBusy, setAdjustPreviewBusy] = useState(false);
  const [cashHandback, setCashHandback] = useState(false);
  const [deliveryConfirmed, setDeliveryConfirmed] = useState(false);
  const [kdsStale, setKdsStale] = useState(false);
  const [kdsSyncedAt, setKdsSyncedAt] = useState<Date | null>(null);
  const [settings, setSettings] = useState<CashSettings | null>(null);
  const [selectedTableID, setSelectedTableID] = useState('');
  const [detail, setDetail] = useState<SessionOperations | null>(null);
  const [orderPayment, setOrderPayment] = useState<{ orderID: string; status: CheckoutStatus } | null>(null);
  const [paymentBusyOrderID, setPaymentBusyOrderID] = useState('');
  const [paymentError, setPaymentError] = useState('');
  const [paymentErrorOrderID, setPaymentErrorOrderID] = useState('');
  const [dialog, setDialog] = useState<Dialog>(null);
  const [cashPreview, setCashPreview] = useState<CashPreview | null>(null);
  const [tendered, setTendered] = useState('');
  const [registerRef, setRegisterRef] = useState('');
  const [receipt, setReceipt] = useState<Receipt | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const openerRef = useRef<HTMLButtonElement | null>(null);
  const kdsPoll = useRef<Promise<StaffOrder[] | null> | null>(null);
  const pendingWrite = useRef<PendingWrite | null>(null);
  const writeInitiated = useRef(false);
  const adjustPreviewRequest = useRef(0);
  const epoch = useRef(0);
  const active = useRef<ActiveStaffContext | null>(null);
  const restored = useRef(false);
  const tableRequest = useRef(0);
  const kdsRequest = useRef(0);
  const settingsRequest = useRef(0);
  const detailRequest = useRef(0);
  const reviewRequest = useRef(0);
  const paymentRequest = useRef(0);
  const hasTables = !!capabilities && (capabilities.can_cash || capabilities.can_serve || capabilities.can_manage);

  useEffect(() => { document.documentElement.lang = locale === 'ja' ? 'ja' : 'zh-Hant'; }, [locale]);
  useEffect(() => {
    if (dialog !== null || busy || !openerRef.current) return;
    const opener = openerRef.current;
    openerRef.current = null;
    if (opener.isConnected && !opener.disabled) opener.focus();
  }, [busy, dialog]);

  const isCurrent = useCallback((requestEpoch: number, ctx: StaffContext, bearer: string) => {
    const current = active.current;
    return !!current?.ready && current.epoch === requestEpoch && current.bearer === bearer && current.context.tenantID === ctx.tenantID && current.context.storeID === ctx.storeID;
  }, []);
  const currentEpoch = useCallback((ctx: StaffContext, bearer: string) => {
    const current = active.current;
    return current?.ready && current.bearer === bearer && current.context.tenantID === ctx.tenantID && current.context.storeID === ctx.storeID ? current.epoch : null;
  }, []);

  const table = useMemo(() => tables.find((item) => item.table_id === selectedTableID) ?? null, [tables, selectedTableID]);
  const selectedTableRef = useRef(selectedTableID);
  selectedTableRef.current = selectedTableID;
  const selectedSessionRef = useRef(table?.session_id ?? '');
  selectedSessionRef.current = table?.session_id ?? '';
  const clearPaymentView = () => {
    paymentRequest.current++; setOrderPayment(null); setPaymentBusyOrderID(''); setPaymentError(''); setPaymentErrorOrderID('');
  };
  const clearLocalSession = useCallback((preserveContext: boolean) => {
    epoch.current += 1;
    paymentRequest.current++; setOrderPayment(null); setPaymentBusyOrderID(''); setPaymentError(''); setPaymentErrorOrderID('');
    active.current = null;
    tableRequest.current += 1; kdsRequest.current += 1; settingsRequest.current += 1; detailRequest.current += 1; reviewRequest.current += 1;
    if (!preserveContext) sessionStorage.removeItem(CONTEXT_KEY);
    sessionStorage.removeItem(TOKEN_KEY);
    openerRef.current = null; kdsPoll.current = null;
    adjustPreviewRequest.current++; setAdjustPreviewBusy(false);
    setConnected(false); setCapabilities(null); setToken('');
    if (!preserveContext) setContext({ tenantID: '', storeID: '' });
    setTables([]); setKdsOrders([]); setReviewOrders([]); setReviewLoaded(false); setReviewLoading(false); setReviewStale(false); setReviewError('');
    setApproveOrder(null); setApproveReason(''); setVoidOrder(null); setVoidReason(''); setAdjustOrder(null); setStopOrder(null); setStopMode(null); setStopRequestID('');
    setAdjustReason(''); setCancelQty([]); setAdjustPreview(null); setCashHandback(false); setDeliveryConfirmed(false); setKdsStale(false); setKdsSyncedAt(null);
    setDetail(null); setSelectedTableID(''); setSettings(null); setCashPreview(null); setReceipt(null); setDialog(null); setError(''); setNotice('');
    setLoading(false); setBusy(false); pendingWrite.current = null; writeInitiated.current = false; setUncertain(false); setAuthPrompt(null);
  }, []);
  const expireCookieSession = useCallback(() => {
    clearLocalSession(true);
    setAuthState({ kind: 'signed-out' });
    setAuthPrompt('login');
  }, [clearLocalSession]);
  const refreshAuthPrompt = useCallback(async () => {
    const next = await probeStaffAuth();
    setAuthState(next);
    if (next.kind === 'signed-out') expireCookieSession();
    else if (next.kind === 'signed-in' && !next.stepUpFresh) setAuthPrompt('step-up');
    else setAuthPrompt(null);
  }, [expireCookieSession]);
  const retryAuthProbe = useCallback(async () => {
    setLoading(true);
    try { await refreshAuthPrompt(); }
    finally { setLoading(false); }
  }, [refreshAuthPrompt]);
  useEffect(() => {
    if (!cookieMode) return;
    return setCookieSessionUnauthorizedHandler(expireCookieSession);
  }, [cookieMode, expireCookieSession]);
  useEffect(() => { clearPaymentView(); }, [connected, selectedTableID, table?.session_id]);

  const loadTables = useCallback(async (ctx: StaffContext, bearer: string) => {
    const requestEpoch = currentEpoch(ctx, bearer);
    if (requestEpoch === null) return null;
    const requestID = ++tableRequest.current;
    let result;
    try { result = await operationsClient.tables(ctx, bearer); }
    catch (cause) { if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== tableRequest.current) return null; throw cause; }
    if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== tableRequest.current) return null;
    setTables(result.tables);
    return result.tables;
  }, [currentEpoch, isCurrent]);
  const loadKds = useCallback(async (ctx: StaffContext, bearer: string, signal?: AbortSignal) => {
    const requestEpoch = currentEpoch(ctx, bearer);
    if (requestEpoch === null) return null;
    const requestID = ++kdsRequest.current;
    let result;
    try { result = await operationsClient.kds(ctx, bearer, signal); }
    catch (cause) { if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== kdsRequest.current) return null; throw cause; }
    if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== kdsRequest.current) return null;
    setKdsOrders(result.orders);
    setKdsStale(false); setKdsSyncedAt(new Date());
    return result.orders;
  }, [currentEpoch, isCurrent]);
  // Every KDS load goes through one in-flight request with an abort watchdog, so polls, refreshes and post-write
  // reloads never overlap or supersede each other. `fresh` waits for an in-flight load and then starts a new one.
  const guardedKds = useCallback(async (ctx: StaffContext, bearer: string, fresh = false): Promise<StaffOrder[] | null> => {
    if (kdsPoll.current) {
      if (!fresh) return kdsPoll.current;
      await kdsPoll.current.catch(() => null);
      if (kdsPoll.current) return kdsPoll.current;
    }
    // Abort rather than race, so the guard is only released once the request itself has settled.
    const controller = new AbortController();
    const watchdog = window.setTimeout(() => controller.abort(), KDS_POLL_TIMEOUT_MS);
    const load = loadKds(ctx, bearer, controller.signal).finally(() => { window.clearTimeout(watchdog); if (kdsPoll.current === load) kdsPoll.current = null; });
    kdsPoll.current = load;
    return load;
  }, [loadKds]);
  const loadSettings = useCallback(async (ctx: StaffContext, bearer: string) => {
    const requestEpoch = currentEpoch(ctx, bearer);
    if (requestEpoch === null) return null;
    const requestID = ++settingsRequest.current;
    let result;
    try { result = await operationsClient.cashSettings(ctx, bearer); }
    catch (cause) { if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== settingsRequest.current) return null; throw cause; }
    if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== settingsRequest.current) return null;
    setSettings(result);
    return result;
  }, [currentEpoch, isCurrent]);
  const loadReviews = useCallback(async (ctx: StaffContext, bearer: string) => {
    const requestEpoch = currentEpoch(ctx, bearer);
    if (requestEpoch === null) return null;
    const requestID = ++reviewRequest.current;
    setReviewLoading(true); setReviewError('');
    try {
      const result = await operationsClient.reviews(ctx, bearer);
      if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== reviewRequest.current) return null;
      setReviewOrders(result.orders.filter((order) => order.refunds?.some((refund) => refund.status !== 'SUCCEEDED') || order.payment_status === 'PAID' && order.payment_review_required && order.review_decision === 'UNRESOLVED' || order.cancellation_requests?.some((request) => request.status === 'REQUESTED' || request.status === 'KITCHEN_CONFIRMED' || !!request.manual_exception_reason)));
      setReviewLoaded(true); setReviewStale(false);
      return result.orders;
    } catch (cause) {
      if (isCurrent(requestEpoch, ctx, bearer) && requestID === reviewRequest.current) {
        setReviewStale(reviewLoaded); setReviewError(t.reviewLoadError);
      }
      throw cause;
    } finally {
      if (requestID === reviewRequest.current) setReviewLoading(false);
    }
  }, [currentEpoch, isCurrent, reviewLoaded, t.reviewLoadError]);
  const refreshReviews = useCallback(async () => {
    const requestEpoch = currentEpoch(context, token);
    try { await loadReviews(context, token); }
    catch { if (requestEpoch !== null && !isCurrent(requestEpoch, context, token)) return; }
  }, [context, currentEpoch, isCurrent, loadReviews, token]);
  const loadDetail = useCallback(async (ctx: StaffContext, bearer: string, tableID: string, sessionID: string) => {
    const requestEpoch = currentEpoch(ctx, bearer);
    if (requestEpoch === null) return null;
    const requestID = ++detailRequest.current;
    let result;
    try { result = await operationsClient.session(ctx, bearer, tableID, sessionID); }
    catch (cause) { if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== detailRequest.current) return null; throw cause; }
    if (!isCurrent(requestEpoch, ctx, bearer) || requestID !== detailRequest.current) return null;
    setDetail(result);
    return result;
  }, [currentEpoch, isCurrent]);

  const inspectPayment = useCallback(async (order: StaffOrder, reconcile = false) => {
    if (order.payment_method !== 'STRIPE' || !table?.session_id || !capabilities?.can_cash && !capabilities?.can_serve) return;
    const requestEpoch = currentEpoch(context, token);
    if (requestEpoch === null) return;
    const tableID = table.table_id;
    const sessionID = table.session_id;
    const requestID = ++paymentRequest.current;
    const isPaymentViewCurrent = () => isCurrent(requestEpoch, context, token)
      && selectedTableRef.current === tableID && selectedSessionRef.current === sessionID
      && paymentRequest.current === requestID;
    setPaymentBusyOrderID(order.id); setPaymentErrorOrderID(order.id); setPaymentError('');
    try {
      const status = reconcile
        ? await checkoutClient.reconcile(context, token, tableID, sessionID, order.id)
        : await checkoutClient.staffStatus(context, token, tableID, sessionID, order.id);
      if (isPaymentViewCurrent()) setOrderPayment({ orderID: order.id, status });
    } catch {
      if (isPaymentViewCurrent()) setPaymentError(t.paymentError);
    } finally {
      if (isPaymentViewCurrent()) setPaymentBusyOrderID('');
    }
  }, [capabilities?.can_cash, capabilities?.can_serve, context, currentEpoch, isCurrent, t.paymentError, table, token]);

  const connect = useCallback(async (ctx: StaffContext, bearer: string) => {
    if (!ctx.tenantID.trim() || !ctx.storeID.trim() || (!cookieMode && !bearer.trim())) return;
    const cleanContext = { tenantID: ctx.tenantID.trim(), storeID: ctx.storeID.trim() };
    const cleanBearer = bearer.trim();
    const requestEpoch = ++epoch.current;
    active.current = { epoch: requestEpoch, context: cleanContext, bearer: cleanBearer, ready: false };
    sessionStorage.setItem(CONTEXT_KEY, JSON.stringify(cleanContext));
    if (cookieMode) sessionStorage.removeItem(TOKEN_KEY);
    else sessionStorage.setItem(TOKEN_KEY, cleanBearer);
    setContext(cleanContext);
    setToken(cleanBearer);
    setStopOrder(null); setStopMode(null); setStopRequestID(''); setAdjustReason(''); setDeliveryConfirmed(false); setCashHandback(false); setDialog(null);
    paymentRequest.current++; setOrderPayment(null); setPaymentBusyOrderID(''); setPaymentError(''); setPaymentErrorOrderID('');
    openerRef.current = null; kdsPoll.current = null;
    reviewRequest.current++; setReviewOrders([]); setReviewLoaded(false); setReviewLoading(false); setReviewStale(false); setReviewError('');
    setCapabilities(null); setTables([]); setKdsOrders([]); setApproveOrder(null); setApproveReason(''); setKdsStale(false); setKdsSyncedAt(null); setSettings(null); setDetail(null); setSelectedTableID(''); setCashPreview(null); setReceipt(null); setDialog(null); setNotice('');
    pendingWrite.current = null; setBusy(false); setUncertain(false);
    setLoading(true); setError(''); setNotice('');
    try {
      const current = await operationsClient.access(cleanContext, cleanBearer);
      if (active.current?.epoch !== requestEpoch) return;
      active.current.ready = true;
      setCapabilities(current); setConnected(true);
      const tasks: Promise<unknown>[] = [];
      if (current.can_cash || current.can_serve || current.can_manage) tasks.push(loadTables(cleanContext, cleanBearer));
      if (current.can_kitchen) tasks.push(guardedKds(cleanContext, cleanBearer));
      if (current.can_manage) tasks.push(loadSettings(cleanContext, cleanBearer));
      const results = await Promise.allSettled(tasks);
      if (!isCurrent(requestEpoch, cleanContext, cleanBearer)) return;
      if (results.some((result) => result.status === 'rejected')) setError(t.error);
      if (current.can_kitchen && (initialView === 'kds' || (!current.can_serve && !current.can_cash && !current.can_manage))) setView('kds');
    } catch (cause) {
      if (active.current?.epoch === requestEpoch) {
        active.current = null; setConnected(false); setCapabilities(null);
        if (!cookieMode) { sessionStorage.removeItem(CONTEXT_KEY); sessionStorage.removeItem(TOKEN_KEY); }
        setError(cause instanceof ApiError && (cause.status === 401 || cause.status === 403) ? t.accessError : explain(cause, locale));
        if (cookieMode && cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) void refreshAuthPrompt();
      }
    } finally { if (epoch.current === requestEpoch) setLoading(false); }
  }, [cookieMode, guardedKds, initialView, isCurrent, loadSettings, loadTables, locale, refreshAuthPrompt, t.accessError, t.error]);

  useEffect(() => {
    if (restored.current) return;
    const storedContext = sessionStorage.getItem(CONTEXT_KEY);
    const storedToken = cookieMode ? null : sessionStorage.getItem(TOKEN_KEY);
    if (cookieMode) sessionStorage.removeItem(TOKEN_KEY);
    if (cookieMode && authState.kind !== 'signed-in') return;
    restored.current = true;
    if (!cookieMode && !storedToken) return;
    if (!storedContext) return;
    try {
      const saved = JSON.parse(storedContext) as StaffContext;
      if (saved.tenantID && saved.storeID) void connect(saved, cookieMode ? '' : storedToken!);
    } catch { sessionStorage.removeItem(CONTEXT_KEY); }
  }, [authState.kind, connect, cookieMode]);

  const refreshDashboard = useCallback(async () => {
    const requestEpoch = currentEpoch(context, token);
    if (!connected || !capabilities || requestEpoch === null) return;
    setLoading(true); setError('');
    try {
      const tasks: Promise<unknown>[] = [];
      if (capabilities.can_cash || capabilities.can_serve || capabilities.can_manage) tasks.push(loadTables(context, token));
      if (capabilities.can_kitchen) tasks.push(guardedKds(context, token, true));
      if (capabilities.can_manage) tasks.push(loadSettings(context, token));
      if (view === 'reviews') tasks.push(loadReviews(context, token));
      const results = await Promise.all(tasks);
      if (!isCurrent(requestEpoch, context, token)) return;
      if (selectedTableID) {
        const freshTables = results.find((result): result is StaffTable[] => Array.isArray(result)) ?? tables;
        const selected = freshTables.find((item) => item.table_id === selectedTableID);
        if (selected?.session_id) await loadDetail(context, token, selected.table_id, selected.session_id);
      }
    } catch (cause) { if (isCurrent(requestEpoch, context, token)) setError(explain(cause, locale)); }
    finally { if (isCurrent(requestEpoch, context, token)) setLoading(false); }
  }, [capabilities, connected, context, currentEpoch, guardedKds, isCurrent, loadDetail, loadReviews, loadSettings, loadTables, locale, selectedTableID, tables, token, view]);

  useEffect(() => {
    if (!connected || !capabilities?.can_kitchen || view !== 'kds') return;
    let timer: number | undefined;
    const schedule = () => {
      if (timer !== undefined) window.clearInterval(timer);
      if (document.visibilityState === 'visible') timer = window.setInterval(() => {
        if (kdsPoll.current) return;
        // loadKds only rethrows for the current connection, so a failure inherited across a KDS remount still marks the board stale.
        void guardedKds(context, token).catch(() => setKdsStale(true));
      }, 2500);
    };
    schedule(); document.addEventListener('visibilitychange', schedule);
    return () => { document.removeEventListener('visibilitychange', schedule); if (timer !== undefined) window.clearInterval(timer); };
  }, [capabilities?.can_kitchen, connected, context, guardedKds, token, view]);

  const refreshDetail = useCallback(async () => {
    const requestEpoch = currentEpoch(context, token);
    if (table?.session_id && requestEpoch !== null) return loadDetail(context, token, table.table_id, table.session_id);
    return null;
  }, [context, currentEpoch, loadDetail, table, token]);

  useEffect(() => {
    if (!connected || !hasTables || view !== 'tables' || !selectedTableID || !detail || dialog || busy || uncertain) return;
    let timer: number | undefined;
    const schedule = () => {
      if (timer !== undefined) window.clearInterval(timer);
      if (document.visibilityState === 'visible') timer = window.setInterval(() => { void refreshDetail().catch(() => undefined); }, 2500);
    };
    schedule(); document.addEventListener('visibilitychange', schedule);
    return () => { document.removeEventListener('visibilitychange', schedule); if (timer !== undefined) window.clearInterval(timer); };
  }, [busy, connected, detail, dialog, hasTables, refreshDetail, selectedTableID, uncertain, view]);

  const perform = useCallback(async (operation: PendingWrite) => {
    const requestEpoch = operation.epoch;
    setBusy(true); setError(''); setNotice('');
    try {
      const result = await operation.run();
      if (epoch.current !== requestEpoch) return;
      pendingWrite.current = null; setUncertain(false);
      try { await operation.success(result); }
      catch (successCause) { if (epoch.current === requestEpoch) setError(explain(successCause, locale)); }
    } catch (cause) {
      if (epoch.current !== requestEpoch) return;
      if (cause instanceof ApiError && cause.status === 409) {
        pendingWrite.current = null; setUncertain(false);
        setError(cause.body.error === 'void_not_eligible' ? t.voidIneligible : ['stop_request_pending', 'stop_request_state', 'manual_exception_required'].includes(cause.body.error) ? explain(cause, locale) : cause.body.error === 'company_hold_active' ? t.companyHoldActive : cause.body.error === 'review_not_ready' ? t.reviewNotReady : cause.body.error === 'stale_version' || cause.body.error === 'idempotency_conflict' ? t.reviewStale : t.stale);
        try { await operation.conflict(); }
        catch (conflictCause) { if (epoch.current === requestEpoch) setError(explain(conflictCause, locale)); }
      } else if (!(cause instanceof ApiError) || cause.status >= 500) {
        setUncertain(true); setError(operation.unavailableCopy?.() ?? t.network);
      } else {
        pendingWrite.current = null; setUncertain(false); setError(explain(cause, locale));
        if (cookieMode && cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) void refreshAuthPrompt();
      }
    } finally { writeInitiated.current = false; if (epoch.current === requestEpoch) setBusy(false); }
  }, [cookieMode, locale, refreshAuthPrompt, t.companyHoldActive, t.network, t.reviewNotReady, t.reviewStale, t.stale, t.voidIneligible]);

  const startWrite = <P, R>(payload: P, execute: (payload: P, key: string) => Promise<R>, success: (result: R) => Promise<void> | void, conflict: () => Promise<void>, key = newOperationKey(), unavailableCopy?: () => string) => {
    if (busy || uncertain || writeInitiated.current) return;
    writeInitiated.current = true;
    const replay = createReplayOperation(key, payload, (original) => execute(original, key));
    const operationEpoch = epoch.current;
    const pending: PendingWrite = { key, epoch: operationEpoch, run: replay.run, success: (value) => success(value as R), conflict, unavailableCopy };
    pendingWrite.current = pending;
    void perform(pending);
  };

  const retryWrite = () => { if (pendingWrite.current && !busy) void perform(pendingWrite.current); };

  const enterTable = async (item: StaffTable) => {
    const requestEpoch = currentEpoch(context, token);
    if (requestEpoch === null || busy || uncertain) return;
    paymentRequest.current++; setOrderPayment(null); setPaymentBusyOrderID(''); setPaymentError(''); setPaymentErrorOrderID('');
    setSelectedTableID(item.table_id); setError(''); setNotice(''); setCashPreview(null); setReceipt(null);
    if (item.session_id) {
      setLoading(true);
      try { await loadDetail(context, token, item.table_id, item.session_id); }
      catch (cause) { if (isCurrent(requestEpoch, context, token)) setError(explain(cause, locale)); }
      finally { if (isCurrent(requestEpoch, context, token)) setLoading(false); }
    } else setDetail(null);
  };

  const openTable = (item: StaffTable) => startWrite<Record<string, never>, OpenedSession>({}, (_payload, key) => operationsClient.openSession(context, token, item.table_id, key), async (opened) => {
    const updated = await loadTables(context, token);
    if (!updated) return;
    setSelectedTableID(item.table_id);
    const selected = updated.find((row) => row.table_id === item.table_id);
    const sessionID = selected?.session_id ?? opened.session.id;
    const loadedDetail = await loadDetail(context, token, item.table_id, sessionID);
    if (!loadedDetail) return;
    setNotice(opened.pin ? `${locale === 'ja' ? 'テーブル PIN' : '桌次 PIN'}: ${opened.pin}` : '');
  }, async () => { await loadTables(context, token); });

  const saveSettings = () => {
    if (!settings) return;
    const payload = { enabled: settings.enabled, mode: settings.mode, expected_version: settings.version };
    startWrite<typeof payload, CashSettings>(payload, (body, key) => operationsClient.saveCashSettings(context, token, body, key), async (saved) => { setSettings(saved); setNotice(locale === 'ja' ? '設定を保存しました。' : '設定已儲存。'); }, async () => { await loadSettings(context, token); });
  };

  const openCashPreview = async () => {
    const requestEpoch = currentEpoch(context, token);
    if (!table?.session_id || !capabilities?.can_cash || busy || uncertain || requestEpoch === null) return;
    setBusy(true); setError(''); setReceipt(null);
    try {
      const current = await operationsClient.cashPreview(context, token, table.table_id, table.session_id);
      if (!isCurrent(requestEpoch, context, token)) return;
      setCashPreview(current); setTendered(current.due_jpy); setRegisterRef(''); setDialog('cash');
    } catch (cause) {
      if (!isCurrent(requestEpoch, context, token)) return;
      if (cause instanceof ApiError && cause.status === 409) { setCashPreview(null); await refreshDetail(); if (isCurrent(requestEpoch, context, token)) setError(t.stale); }
      else setError(explain(cause, locale));
    } finally { if (isCurrent(requestEpoch, context, token)) setBusy(false); }
  };

  const settleCash = () => {
    if (!table?.session_id || !cashPreview || !/^[0-9]+$/.test(tendered) || !registerRef.trim()) return;
    const payload = { fingerprint: cashPreview.fingerprint, tendered_jpy: tendered, register_session_ref: registerRef.trim() };
    startWrite(payload, (body, key) => operationsClient.settleCash(context, token, table.table_id, table.session_id!, body, key), async (saved) => {
      setReceipt(saved); setCashPreview(null); await refreshDetail(); await loadTables(context, token);
    }, async () => { setCashPreview(null); setDialog(null); await refreshDetail(); await loadTables(context, token); });
  };

  const closeTable = () => {
    if (!table?.session_id || !capabilities?.can_serve || (detail?.close_blockers.length ?? 0) > 0) return;
    startWrite({}, (_payload, key) => operationsClient.closeSession(context, token, table.table_id, table.session_id!, key), async () => {
      setDialog(null); setDetail(null); setSelectedTableID(''); await loadTables(context, token);
    }, async () => { await refreshDetail(); await loadTables(context, token); });
  };

  const acceptOrder = (order: StaffOrder) => {
    if (!table?.session_id) return;
    const body = { expected_version: order.version };
    startWrite(body, (payload, key) => operationsClient.acceptOrder(context, token, table.table_id, table.session_id!, order.id, payload.expected_version, key), async () => { await refreshDetail(); await loadTables(context, token); }, async () => { await refreshDetail(); await loadTables(context, token); });
  };

  const approveFulfillment = () => {
    if (!approveOrder || !capabilities?.can_approve_fulfillment || !approveReason.trim()) return;
    const target = approveOrder;
    const payload = { expected_version: target.version, reason: approveReason.trim() };
    startWrite(payload, (body, key) => operationsClient.approveFulfillment(context, token, target, body, key), async () => {
      setDialog(null); setApproveOrder(null); setApproveReason('');
      const tasks: Promise<unknown>[] = [loadReviews(context, token)];
      if (capabilities.can_kitchen) tasks.push(guardedKds(context, token, true));
      if (capabilities.can_cash || capabilities.can_serve || capabilities.can_manage) tasks.push(loadTables(context, token));
      await Promise.all(tasks);
    }, async () => {
      setDialog(null); setApproveOrder(null); setApproveReason('');
      await loadReviews(context, token);
    });
  };

  const openVoidDialog = (order: StaffOrder, button: HTMLButtonElement) => {
    openerRef.current = button; voidKey.current = newOperationKey(); setVoidOrder(order); setVoidReason(''); setError(''); setDialog('void');
  };
  const voidUnpaidOrder = () => {
    if (!voidOrder || !capabilities?.can_approve_fulfillment || !voidReason.trim() || !voidKey.current) return;
    const target = voidOrder; const payload = { expected_version: target.version, reason: voidReason.trim() };
    startWrite(payload, (body, key) => operationsClient.voidUnpaidOrder(context, token, target, body, key), async () => {
      setDialog(null); setVoidOrder(null); setVoidReason(''); voidKey.current = '';
      await Promise.all([refreshDetail(), loadTables(context, token)]);
    }, async () => {
      setDialog(null); setVoidOrder(null); setVoidReason(''); voidKey.current = '';
      await Promise.all([refreshDetail(), loadTables(context, token)]);
    }, voidKey.current, () => t.voidUnavailable);
  };

  const adjustmentBody = (): AdjustmentRequest | null => {
    if (!adjustOrder || !adjustReason.trim()) return null;
    const lines = cancelQty.flatMap((qty, index) => qty > 0 ? [{ line_index: adjustOrder.lines[index]?.line_index ?? index, cancelled_quantity: qty }] : []);
    return lines.length ? { expected_version: adjustOrder.version, reason: adjustReason.trim(), lines, cash_hand_back_confirmed: cashHandback } : null;
  };
  const invalidateAdjustmentPreview = () => {
    adjustPreviewRequest.current++;
    setAdjustPreview(null);
    setAdjustPreviewBusy(false);
  };
  const previewOrderAdjustment = async () => {
    const body = adjustmentBody();
    if (!adjustOrder || !body || body.lines.some((line) => line.cancelled_quantity > (adjustOrder.lines.find((item, i) => (item.line_index ?? i) === line.line_index)?.quantity ?? 0))) return;
    const requestID = ++adjustPreviewRequest.current;
    const requestEpoch = epoch.current;
    const targetID = adjustOrder.id;
    setError(''); setAdjustPreview(null); setAdjustPreviewBusy(true);
    try {
      const preview = await operationsClient.previewAdjustment(context, token, adjustOrder, body);
      if (requestID === adjustPreviewRequest.current && requestEpoch === epoch.current && adjustOrder?.id === targetID) setAdjustPreview(preview);
    } catch (cause) {
      if (requestID === adjustPreviewRequest.current && requestEpoch === epoch.current) setError(explain(cause, locale));
    } finally {
      if (requestID === adjustPreviewRequest.current) setAdjustPreviewBusy(false);
    }
  };
  const confirmOrderAdjustment = () => {
    const target = adjustOrder; const payload = adjustmentBody();
    if (!target || !payload || !adjustPreview || target.payment_method === 'CASH' && target.payment_status === 'PAID' && !cashHandback) return;
    startWrite(payload, (body, key) => operationsClient.adjustOrder(context, token, target, body, key), async () => {
      invalidateAdjustmentPreview(); setDialog(null); setAdjustOrder(null); setCancelQty([]); setAdjustReason('');
      if (table?.session_id === target.session_id) await refreshDetail();
      await Promise.all([loadReviews(context, token), loadTables(context, token)]);
      if (capabilities?.can_kitchen) await guardedKds(context, token, true);
    }, async () => {
      invalidateAdjustmentPreview();
      const [reviews, session] = await Promise.all([loadReviews(context, token), table?.session_id ? refreshDetail() : Promise.resolve(null)]);
      const latest = session?.orders.find((item) => item.id === target.id) ?? reviews?.find((item) => item.id === target.id);
      if (latest) { setAdjustOrder(latest); setCancelQty(latest.lines.map(() => 0)); }
    });
  };
  const retryRefund = (order: StaffOrder, refundID: string) => startWrite({ expected_version: order.version }, (body, key) => operationsClient.retryRefund(context, token, order, refundID, body.expected_version, key), async () => { await Promise.all([loadReviews(context, token), table?.session_id ? refreshDetail() : Promise.resolve()]); }, async () => { await Promise.all([loadReviews(context, token), table?.session_id ? refreshDetail() : Promise.resolve()]); });
  const acknowledgeUpdatedOrder = (order: StaffOrder) => startWrite({ expected_version: order.version }, (body, key) => operationsClient.acknowledgeOrderUpdate(context, token, order, body.expected_version, key), async () => { await guardedKds(context, token, true); }, async () => { await guardedKds(context, token, true); });

  const submitStopRequest = () => {
    if (!stopOrder || !adjustReason.trim()) return;
    const lines = cancelQty.flatMap((quantity, index) => quantity > 0 && stopOrder.lines[index]?.stop_request_eligibility === 'ELIGIBLE' ? [{ line_index: stopOrder.lines[index]?.line_index ?? index, cancelled_quantity: quantity }] : []);
    if (!lines.length) return;
    const target = stopOrder; const body = { expected_version: target.version, reason: adjustReason.trim(), lines }; const writeEpoch = epoch.current;
    startWrite(body, (payload, key) => operationsClient.requestStop(context, token, target, payload, key), async () => { setDialog(null); setStopOrder(null); setStopMode(null); await refreshStopData(target); }, async () => { try { await refreshStopData(target); } finally { if (epoch.current === writeEpoch) clearStopDialog(); } });
  };
  const openStopAction = (order: StaffOrder, requestID: string, action: 'withdraw' | 'confirm' | 'reject' | 'finalize', button?: HTMLButtonElement) => {
    openerRef.current = button ?? (document.activeElement instanceof HTMLButtonElement ? document.activeElement : null);
    setStopOrder(order); setStopRequestID(requestID); setStopMode(action); setAdjustReason(''); setCashHandback(false); setDeliveryConfirmed(false); setDialog('stop');
  };
  const stopAction = (order: StaffOrder, requestID: string, action: 'withdraw' | 'confirm' | 'reject' | 'finalize', reason = '', nonDelivery = true) => {
    const item = order.cancellation_requests?.find((request) => request.id === requestID); if (!item) return;
    const body = { expected_version: order.version, expected_request_version: item.version, ...(reason ? { reason } : {}), ...(action === 'finalize' ? { non_delivery_confirmed: nonDelivery, cash_hand_back_confirmed: nonDelivery && stopNeedsCashHandback(order, requestID) ? cashHandback : false } : {}) };
    const writeEpoch = epoch.current;
    startWrite(body, (payload, key) => operationsClient.stopRequestAction(context, token, order, requestID, action, payload, key), async () => { setDialog(null); setStopOrder(null); setStopMode(null); await refreshStopData(order); }, async () => { try { await refreshStopData(order); } finally { if (epoch.current === writeEpoch) clearStopDialog(); } });
  };
  const refreshStopData = async (order: StaffOrder) => {
    await Promise.all([loadReviews(context, token), table?.session_id === order.session_id ? refreshDetail() : Promise.resolve(null), capabilities?.can_kitchen ? guardedKds(context, token, true) : Promise.resolve(null)]);
  };
  const clearStopDialog = () => { setDialog(null); setStopOrder(null); setStopMode(null); setStopRequestID(''); setAdjustReason(''); setCancelQty([]); setDeliveryConfirmed(false); setCashHandback(false); };

  const advanceOrder = (order: StaffOrder, target: 'PREPARING' | 'READY' | 'SERVED') => {
    const body = { expected_version: order.version, target };
    const success = async () => { await refreshDetail(); await loadTables(context, token); if (capabilities?.can_kitchen) await guardedKds(context, token, true); };
    const conflict = async () => { await success(); };
    startWrite(body, (payload, key) => {
      return operationsClient.advanceOrder(context, token, order.table_id, order.session_id, order.id, target, payload.expected_version, key);
    }, success, conflict);
  };

  const disconnect = () => {
    clearLocalSession(false);
  };
  const logout = () => { disconnect(); submitStaffLogout(); };

  const canReadReviews = !!capabilities && (capabilities.can_cash || capabilities.can_serve || capabilities.can_manage || capabilities.can_kitchen || capabilities.can_approve_fulfillment === true);
  const noOperations = !!capabilities && !hasTables && !capabilities.can_kitchen && !canReadReviews;
  const boardOrders = kdsOrders.filter((order) => (['ACCEPTED', 'PREPARING', 'READY'].includes(order.fulfillment_status) || order.fulfillment_status === 'CANCELLED' && order.order_updated)).sort((a, b) => a.created_at.localeCompare(b.created_at));
  const blockers = detail?.close_blockers ?? [];

  if (!connected) return <StaffShell demo={demo} locale={locale} setLocale={setLocale} t={t}>
    {cookieMode && authState.kind === 'unavailable' ? <section className="entry-card staff-entry">
      <div className="eyebrow">{t.subtitle}</div><h1>{t.connect}</h1>
      <p className="error" role="alert">{t.authUnavailable}</p>
      <button className="button primary wide" type="button" disabled={loading} onClick={() => void retryAuthProbe()}>{loading ? t.loading : t.retryAuth}</button>
    </section> : cookieMode && authState.kind === 'signed-out' ? <section className="entry-card staff-entry">
      <div className="eyebrow">{t.subtitle}</div><h1>{t.login}</h1><p>{t.loginIntro}</p>
      <a className="button primary wide" href={staffLoginURL(window.location.pathname + window.location.search)}>{t.login}</a>
      {error && <p className="error" role="alert">{error}</p>}
    </section> : <form className="entry-card staff-entry" onSubmit={(event) => { event.preventDefault(); void connect(context, cookieMode ? '' : token); }}>
      <div className="eyebrow">{t.subtitle}</div><h1>{t.connect}</h1>
      <p>{cookieMode ? t.loginIntro : locale === 'ja' ? '既存のスタッフ認証トークンで店舗に接続します。' : '使用既有店員權杖連線至分店。'}</p>
      {cookieMode && authPrompt === 'step-up' && <p className="notice" role="status">{t.stepUpNotice} <a className="button quiet small" href={staffStepUpURL(window.location.pathname + window.location.search)}>{t.stepUp}</a></p>}
      <label className="field">{t.tenant}<input autoComplete="organization" value={context.tenantID} onChange={(event) => setContext((current) => ({ ...current, tenantID: event.target.value }))} required /></label>
      <label className="field">{t.store}<input autoComplete="off" value={context.storeID} onChange={(event) => setContext((current) => ({ ...current, storeID: event.target.value }))} required /></label>
      {!cookieMode && <label className="field">{t.token}<input type="password" autoComplete="current-password" value={token} onChange={(event) => setToken(event.target.value)} required /></label>}
      <button className="button primary wide" disabled={loading}>{loading ? t.loading : t.connect}</button>
      {error && <p className="error" role="alert">{error}</p>}
    </form>}
  </StaffShell>;

  const stopCashHandbackRequired = stopNeedsCashHandback(stopOrder, stopRequestID);
  return <StaffShell demo={demo} locale={locale} setLocale={setLocale} t={t}>
    <div className="workspace staff-workspace">
      <header className="welcome"><div><div className="eyebrow">{t.subtitle}</div><h1>{context.storeID}</h1><p>{context.tenantID}</p></div><button className="text-button" onClick={cookieMode ? logout : disconnect}>{cookieMode ? t.logout : t.disconnect}</button></header>
      {cookieMode && authPrompt === 'step-up' && <p className="notice" role="status">{t.stepUpNotice} <a className="button quiet small" href={staffStepUpURL(window.location.pathname + window.location.search)}>{t.stepUp}</a></p>}
      {cookieMode && authPrompt === 'login' && <p className="notice" role="status">{t.loginIntro} <a className="button primary small" href={staffLoginURL(window.location.pathname + window.location.search)}>{t.login}</a></p>}
      {noOperations ? <p className="notice">{t.noAccess}</p> : <>
        <nav className="tabs staff-tabs" aria-label={locale === 'ja' ? 'スタッフ操作' : '店員操作'}>
          {hasTables && <button className={view === 'tables' ? 'tab active' : 'tab'} disabled={busy || uncertain} onClick={() => { clearPaymentView(); setView('tables'); setSelectedTableID(''); setDetail(null); }}>{t.tables}</button>}
          {capabilities?.can_kitchen && <button className={view === 'kds' ? 'tab active' : 'tab'} disabled={busy || uncertain} onClick={() => { clearPaymentView(); setView('kds'); setSelectedTableID(''); setDetail(null); }}>{t.kds}</button>}
          {canReadReviews && <button className={view === 'reviews' ? 'tab active' : 'tab'} disabled={busy || uncertain} onClick={() => { clearPaymentView(); setView('reviews'); setSelectedTableID(''); setDetail(null); void refreshReviews(); }}>{t.reviews}{reviewLoaded ? ` · ${reviewOrders.length}` : ''}</button>}
          <button className="icon-button" aria-label={t.refresh} onClick={() => void refreshDashboard()} disabled={loading || busy || uncertain}>↻</button>
        </nav>
        {loading && <p role="status" className="muted">{t.loading}</p>}
        {error && <p className="error" role="alert">{error}</p>}
        {notice && <p className="notice" role="status">{notice}</p>}
        {uncertain && <button className="button retry wide staff-retry" disabled={busy} onClick={retryWrite}>{t.retry}</button>}
        {view === 'tables' && hasTables && <>
          {!detail && <section className="staff-table-view">
            <div className="section-heading"><div><div className="eyebrow">{t.subtitle}</div><h2>{t.tables}</h2></div></div>
            {tables.length === 0 ? <div className="empty-state"><p>{t.noTables}</p></div> : <div className="staff-table-grid">{tables.map((item) => <article className="staff-table-card" key={item.table_id}>
              <div className="staff-table-card-head"><div><h3>{item.name}</h3><span className={`status-chip status-${item.status.toLowerCase()}`}>{tableStatus(item.status, t)}</span></div><span className="table-mode">{modeLabel(item.mode, locale)}</span></div>
              <div className="staff-table-metrics"><span>{t.unpaid}<strong>{amountJPY(item.unpaid_cash_jpy)}</strong></span><span>{t.count}<strong>{item.order_count}</strong></span></div>
              <div className="staff-table-actions">{item.session_id && <button className="button quiet" disabled={busy || uncertain} onClick={() => void enterTable(item)}>{t.enter}</button>}{!item.session_id && item.status !== 'OPEN' && capabilities.can_serve && <button className="button small primary" disabled={busy || uncertain} onClick={() => openTable(item)}>{t.openTable}</button>}</div>
            </article>)}</div>}
            {capabilities?.can_manage && settings && <section className="cash-settings-card"><div className="section-heading"><div><div className="eyebrow">MANAGER</div><h2>{t.cashSettings}</h2></div></div>
              <label className="settings-check"><input type="checkbox" checked={settings.enabled} disabled={busy || uncertain} onChange={(event) => setSettings({ ...settings, enabled: event.target.checked })} />{t.enabled}</label>
              <label className="field">{t.mode}<select value={settings.mode} disabled={busy || uncertain} onChange={(event) => setSettings({ ...settings, mode: event.target.value as CashSettings['mode'] })}><option value="PREPAID">{t.prepaid}</option><option value="PAY_LATER">{t.payLater}</option></select></label>
              <button className="button primary" disabled={busy || uncertain} onClick={saveSettings}>{t.save}</button>
            </section>}
          </section>}
          {detail && table && <section className="staff-detail"><div className="section-heading"><div><div className="eyebrow">{t.session}</div><h2>{table.name}</h2></div><button className="text-button" disabled={busy || uncertain} onClick={() => { clearPaymentView(); setDetail(null); setSelectedTableID(''); }}>{t.back}</button></div>
            <div className="staff-detail-meta"><span>{detail.session.status}</span><span>{modeLabel(detail.session.mode, locale)}</span><span>{table.expires_at ? new Date(table.expires_at).toLocaleString(locale === 'ja' ? 'ja-JP' : 'zh-TW') : ''}</span></div>
            <div className="staff-section-title"><h3>{t.orders}</h3>{capabilities.can_cash && <button className="button small primary" disabled={busy || uncertain} onClick={(event) => { openerRef.current = event.currentTarget; void openCashPreview(); }}>{demo ? t.demoCash : t.cashSettle}</button>}</div>
            {detail.orders.length === 0 ? <div className="empty-state"><p>{t.noOrders}</p></div> : detail.orders.map((order) => <StaffOrderCard key={order.id} order={order} locale={locale} t={t} demo={demo} canServe={capabilities.can_serve} busy={busy || uncertain} paymentStatus={orderPayment?.orderID === order.id ? orderPayment.status : undefined} paymentBusy={paymentBusyOrderID === order.id} paymentError={paymentErrorOrderID === order.id ? paymentError : ''} canCheckPayment={capabilities.can_cash || capabilities.can_serve} canManage={capabilities.can_approve_fulfillment === true} onVoid={(button) => openVoidDialog(order, button)} onStopRequest={(button) => { openerRef.current = button; setStopOrder(order); setStopMode('request'); setAdjustReason(''); setCancelQty(order.lines.map(() => 0)); setDialog('stop'); }} onStopAction={(id, action, button) => openStopAction(order, id, action, button)} onAdjust={() => { invalidateAdjustmentPreview(); setAdjustOrder(order); setAdjustReason(''); setCancelQty(order.lines.map(() => 0)); setCashHandback(false); setError(''); setDialog('adjust'); }} onRetryRefund={(refundID) => retryRefund(order, refundID)} onAcknowledgeUpdate={() => acknowledgeUpdatedOrder(order)} onCheckPayment={() => void inspectPayment(order)} onReconcilePayment={() => void inspectPayment(order, true)} onAccept={() => acceptOrder(order)} onServe={() => advanceOrder(order, 'SERVED')} />)}
            <div className="staff-section-title"><h3>{t.receipts}</h3></div>
            {detail.receipts.map((item) => <ReceiptCard key={item.id} receipt={item} t={t} demo={demo} locale={locale} />)}
            {capabilities.can_serve && <button className="button quiet wide close-table" disabled={busy || uncertain} onClick={(event) => { openerRef.current = event.currentTarget; setDialog('close'); }}>{t.close}</button>}
          </section>}
        </>}
        {view === 'kds' && capabilities?.can_kitchen && <section className="kds-view"><div className="section-heading"><div><div className="eyebrow">{t.subtitle}</div><h2>{t.kds}</h2></div></div>
          {kdsStale && <p className="error kds-stale" role="status">{t.kdsStale}{kdsSyncedAt && <> · {t.kdsLastSync}: {kdsSyncedAt.toLocaleTimeString(locale === 'ja' ? 'ja-JP' : 'zh-TW')}</>}</p>}
          <div className="kds-columns">{(['ACCEPTED', 'PREPARING', 'READY'] as const).map((status) => <section className="kds-column" key={status}><h3>{statusLabel(status, t)}<span>{boardOrders.filter((order) => order.fulfillment_status === status || status === 'ACCEPTED' && order.fulfillment_status === 'CANCELLED' && order.order_updated).length}</span></h3>
            {boardOrders.filter((order) => order.fulfillment_status === status || status === 'ACCEPTED' && order.fulfillment_status === 'CANCELLED' && order.order_updated).map((order) => <StaffOrderCard key={order.id} order={order} locale={locale} t={t} canServe={false} busy={busy || uncertain} compact onStopAction={(id, action, button) => openStopAction(order, id, action, button)} onAcknowledgeUpdate={() => acknowledgeUpdatedOrder(order)} onStart={() => advanceOrder(order, 'PREPARING')} onReady={() => advanceOrder(order, 'READY')} />)}
          </section>)}</div>
        </section>}
        {view === 'reviews' && canReadReviews && <section className="staff-table-view review-queue"><div className="section-heading"><div><div className="eyebrow">{t.subtitle}</div><h2>{t.reviewQueue}</h2></div><button className="button quiet" disabled={loading || reviewLoading || busy || uncertain} onClick={() => void refreshReviews()}>{t.refresh}</button></div>
          {reviewLoading && <p className="muted" role="status">{t.reviewLoading}</p>}
          {reviewError && <p className={reviewStale ? 'notice' : 'error'} role={reviewStale ? 'status' : 'alert'}>{reviewStale ? t.reviewDataStale : t.reviewLoadError}</p>}
          {reviewLoaded && reviewOrders.length === 0 && !reviewLoading ? <div className="empty-state"><p>{t.noReviews}</p></div> : reviewOrders.map((order) => <article className="staff-order-card" key={order.id}>
            <header className="staff-order-head"><div><strong>{order.table_name} · #{order.generation}</strong><small>{new Date(order.created_at).toLocaleString(locale === 'ja' ? 'ja-JP' : 'zh-TW', { timeZone: 'Asia/Tokyo' })}</small></div><span className="status-chip">{t.reviews}</span></header>
            <ul className="staff-order-lines">{order.lines.map((line, index) => <li key={`${line.product_name}-${index}`}><span>{line.product_name} × {line.quantity}{line.options.map((option, optionIndex) => <small key={`${option.group_name}-${option.option_name}-${optionIndex}`}>{option.group_name.trim() ? `${option.group_name}: ` : ''}{option.option_name}</small>)}</span><strong>{amountJPY(line.line_gross_jpy)}</strong></li>)}</ul>
            <StopRequests order={order} t={t} compact={false} canManage={capabilities?.can_approve_fulfillment === true} onAction={(id, action, button) => openStopAction(order, id, action, button)} />
            <div className="staff-order-payment"><span>{paymentLabel(order.payment_status, t)}</span><span>{t.effectiveTotal}</span><strong>{amountJPY(order.effective_total_gross_jpy ?? order.total_gross_jpy)}</strong>{order.effective_total_gross_jpy !== undefined && order.effective_total_gross_jpy !== order.total_gross_jpy && <><span>{t.originalTotal}</span><strong>{amountJPY(order.total_gross_jpy)}</strong></>}</div>
            {order.refunds?.length ? <section className="refund-list"><strong>{t.refunds}</strong>{order.refunds.map((refund) => <div key={refund.id}><span>{amountJPY(refund.amount_jpy)} · {refund.status === 'SUCCEEDED' ? t.refundSucceeded : refund.status === 'FAILED' ? t.refundFailed : refund.safe_error_code === 'REFUND_DISPATCH_PENDING' ? t.refundDispatchPending : refund.safe_error_code === 'HOLD_RECONCILIATION_NEEDED' ? t.refundReconciliationNeeded : t.refundProcessing}</span>{capabilities?.can_approve_fulfillment === true && refund.status === 'FAILED' && <button className="button small retry" disabled={busy || uncertain} onClick={() => retryRefund(order, refund.id)}>{t.retryRefund}</button>}</div>)}</section> : null}
            <p className="notice">{(order.review_reason_codes ?? []).map((code) => code === 'COMPANY_HOLD' ? t.reviewCompany : code === 'OPERATIONAL_HOLD' ? t.reviewOperations : code === 'SESSION_CLOSED' ? t.reviewClosed : code === 'REFUND_UNRESOLVED' ? t.reviewRefund : code === 'STOP_REQUEST_PENDING' ? t.stopRequestPending : code === 'STOP_MANUAL_EXCEPTION' ? t.stopManual : t.reviewPayment).join(' · ')}</p>
            <div className="staff-order-actions">{capabilities?.can_approve_fulfillment === true && order.payment_review_required === true && order.review_decision === 'UNRESOLVED' && order.fulfillment_status === 'WAITING' && <button className="button primary" disabled={busy || uncertain} onClick={(event) => { openerRef.current = event.currentTarget; setApproveOrder(order); setApproveReason(''); setDialog('approve'); }}>{t.approve}</button>}{capabilities?.can_approve_fulfillment === true && order.review_decision === 'UNRESOLVED' && (order.fulfillment_status === 'WAITING' || order.fulfillment_status === 'ACCEPTED') && (order.payment_method === 'CASH' || order.payment_status === 'PAID') && <button className="button quiet" disabled={busy || uncertain} onClick={(event) => { openerRef.current = event.currentTarget; invalidateAdjustmentPreview(); setAdjustOrder(order); setAdjustReason(''); setCancelQty(order.lines.map(() => 0)); setCashHandback(false); setError(''); setDialog('adjust'); }}>{t.adjust}</button>}</div>
          </article>)}
        </section>}
      </>}
    </div>
    {dialog === 'cash' && <StaffDialog labelledBy="cash-title" dismissible={!busy && !uncertain} onDismiss={() => { setDialog(null); setReceipt(null); setCashPreview(null); }}><div className="eyebrow">{demo ? t.demoCashTitle : t.cashTitle}</div><h2 id="cash-title">{receipt ? t.receipt : demo ? t.demoCashTitle : t.cashTitle}</h2>
      {demo && <DemoBanner demo locale={locale} />}
      {receipt ? <ReceiptCard receipt={receipt} t={t} demo={demo} locale={locale} /> : <>
        {!cashPreview ? <p className="muted">{t.loading}</p> : <><p>{t.due} <strong>{amountJPY(cashPreview.due_jpy)}</strong></p><p className="tax-row"><span>{t.tax}</span><span>{amountJPY(cashPreview.tax.tax_jpy)}</span></p><ul className="cash-order-list">{cashPreview.order_ids.map((orderID) => { const order = detail?.orders.find((item) => item.id === orderID); return <li key={orderID}>{order ? `${order.table_name} · #${order.generation} · ${amountJPY(order.effective_total_gross_jpy ?? order.total_gross_jpy)}` : orderID}</li>; })}</ul>
          {cashPreview.order_ids.length === 0 && <p className="notice">{t.noCashOrders}</p>}
          <label className="field">{t.tendered}<input inputMode="numeric" pattern="[0-9]*" value={tendered} disabled={busy || uncertain} onChange={(event) => setTendered(event.target.value)} /></label>
          <label className="field">{t.registerRef}<input value={registerRef} disabled={busy || uncertain} onChange={(event) => setRegisterRef(event.target.value)} /></label>
          <button className="button primary wide" disabled={busy || uncertain || !tenderedEnough(tendered, cashPreview.due_jpy) || !registerRef.trim() || cashPreview.order_ids.length === 0} onClick={settleCash}>{t.settle}</button>
        </>}
      </>}
      <button className="button quiet wide" disabled={busy} onClick={() => { setDialog(null); setReceipt(null); setCashPreview(null); }}>{t.closeDialog}</button>
      {uncertain && <button className="button retry wide" disabled={busy} onClick={retryWrite}>{t.retry}</button>}
      {error && <p className="error" role="alert">{error}</p>}
    </StaffDialog>}
    {dialog === 'close' && <StaffDialog labelledBy="close-title" dismissible={!busy && !uncertain} onDismiss={() => setDialog(null)}><div className="eyebrow">{demo ? t.demoClose : t.close}</div><h2 id="close-title">{t.closeTitle}</h2>{demo && <DemoBanner demo locale={locale} />}
      {blockers.length > 0 ? <><p className="error">{t.blockers}</p><ul className="blocker-list">{blockers.map((blocker, index) => <li key={`${blocker}-${index}`}>{blockerLabel(blocker, locale)}</li>)}</ul></> : <p className="notice">{t.noBlockers}</p>}
      <button className="button primary wide" disabled={busy || uncertain || blockers.length > 0} onClick={closeTable}>{t.closeConfirm}</button>
      <button className="button quiet wide" disabled={busy || uncertain} onClick={() => setDialog(null)}>{t.closeDialog}</button>
      {uncertain && <button className="button retry wide" disabled={busy} onClick={retryWrite}>{t.retry}</button>}
      {error && <p className="error" role="alert">{error}</p>}
    </StaffDialog>}
    {dialog === 'void' && voidOrder && <StaffDialog labelledBy="void-title" dismissible={!busy && !uncertain} onDismiss={() => { setDialog(null); setVoidOrder(null); setVoidReason(''); voidKey.current = ''; }}><div className="eyebrow">{t.orders}</div><h2 id="void-title">{t.voidTitle}</h2><p>{t.voidExplanation}</p><p>{voidOrder.table_name} · #{voidOrder.generation}</p><label className="field">{t.voidReason}<textarea rows={3} required maxLength={500} value={voidReason} disabled={busy || uncertain} onChange={(event) => setVoidReason(event.target.value)} /></label><button className="button primary wide" disabled={busy || uncertain || !voidReason.trim()} onClick={voidUnpaidOrder}>{t.confirmVoid}</button><button className="button quiet wide" disabled={busy || uncertain} onClick={() => { setDialog(null); setVoidOrder(null); setVoidReason(''); voidKey.current = ''; }}>{t.closeDialog}</button>{uncertain && <button className="button retry wide" disabled={busy} onClick={retryWrite}>{t.retry}</button>}{error && <p className="error" role="alert">{error}</p>}</StaffDialog>}
    {dialog === 'approve' && approveOrder && <StaffDialog labelledBy="approve-title" dismissible={!busy && !uncertain} onDismiss={() => { setDialog(null); setApproveOrder(null); }}><div className="eyebrow">{t.reviews}</div><h2 id="approve-title">{t.approveTitle}</h2><p>{approveOrder.table_name} · #{approveOrder.generation} · {amountJPY(approveOrder.effective_total_gross_jpy ?? approveOrder.total_gross_jpy)}</p><label className="field">{t.reason}<textarea rows={3} required maxLength={500} value={approveReason} disabled={busy || uncertain} onChange={(event) => setApproveReason(event.target.value)} /></label><button className="button primary wide" disabled={busy || uncertain || !approveReason.trim()} onClick={approveFulfillment}>{t.confirmApprove}</button><button className="button quiet wide" disabled={busy || uncertain} onClick={() => setDialog(null)}>{t.closeDialog}</button>{uncertain && <button className="button retry wide" disabled={busy} onClick={retryWrite}>{t.retry}</button>}{error && <p className="error" role="alert">{error}</p>}</StaffDialog>}
    {dialog === 'stop' && stopOrder && <StaffDialog labelledBy="stop-title" dismissible={!busy && !uncertain} onDismiss={clearStopDialog}><div className="eyebrow">{t.orders}</div><h2 id="stop-title">{stopMode === 'request' ? t.stopRequestTitle : stopMode === 'reject' ? t.stopRejectTitle : stopMode === 'confirm' ? t.stopConfirmTitle : stopMode === 'withdraw' ? t.stopWithdrawTitle : t.stopFinalized}</h2><p>{stopOrder.table_name} · #{stopOrder.generation}</p>
      {stopMode !== 'request' && (() => { const item = stopOrder.cancellation_requests?.find((request) => request.id === stopRequestID); const line = item && stopOrder.lines.find((candidate, index) => (candidate.line_index ?? index) === item.line_index); return item ? <p className="notice">{line?.product_name ?? `#${item.line_index + 1}`} × {item.quantity} · {item.reason}</p> : null; })()}
      {stopMode === 'request' && <><ul className="adjustment-lines">{stopOrder.lines.map((line, index) => {
        const eligible = line.stop_request_eligibility === 'ELIGIBLE';
        const reason = line.stop_request_eligibility === 'PENDING' ? t.stopPending : line.stop_request_eligibility === 'MANUAL_EXCEPTION' ? t.stopManual : line.stop_request_eligibility === 'NO_REMAINING_QUANTITY' ? t.stopNoRemaining : t.stopUnavailable;
        return <li key={`${line.product_name}-${index}`}><span>{line.product_name} · {line.quantity}{!eligible && <small id={`stop-line-reason-${index}`}> · {reason}</small>}</span><label className="field">{locale === 'ja' ? '停止数量' : '停止數量'}<input type="number" min={0} max={line.quantity} step={1} value={eligible ? cancelQty[index] ?? 0 : 0} disabled={!eligible || busy || uncertain} aria-describedby={!eligible ? `stop-line-reason-${index}` : undefined} onChange={(event) => setCancelQty((current) => current.map((qty, i) => i === index ? Math.max(0, Math.min(line.quantity, Number(event.target.value) || 0)) : qty))} /></label></li>;
      })}</ul><label className="field">{t.stopReason}<textarea rows={3} required maxLength={500} value={adjustReason} disabled={busy || uncertain} onChange={(event) => setAdjustReason(event.target.value)} /></label><button className="button primary wide" disabled={busy || uncertain || !adjustReason.trim() || !cancelQty.some((qty, index) => qty > 0 && stopOrder.lines[index]?.stop_request_eligibility === 'ELIGIBLE')} onClick={submitStopRequest}>{t.stopRequest}</button></>}
      {stopMode === 'reject' && <><label className="field">{t.stopReason}<textarea rows={3} required maxLength={500} value={adjustReason} disabled={busy || uncertain} onChange={(event) => setAdjustReason(event.target.value)} /></label><button className="button primary wide" disabled={busy || uncertain || !adjustReason.trim()} onClick={() => stopAction(stopOrder, stopRequestID, 'reject', adjustReason.trim())}>{t.stopKitchenReject}</button></>}
      {stopMode === 'confirm' && <button className="button primary wide" disabled={busy || uncertain} onClick={() => stopAction(stopOrder, stopRequestID, 'confirm')}>{t.stopKitchenConfirm}</button>}
      {stopMode === 'withdraw' && <button className="button primary wide" disabled={busy || uncertain} onClick={() => stopAction(stopOrder, stopRequestID, 'withdraw')}>{t.stopWithdraw}</button>}
      {stopMode === 'finalize' && <><label className="cash-handback"><input type="checkbox" checked={deliveryConfirmed} disabled={busy || uncertain} onChange={(event) => setDeliveryConfirmed(event.target.checked)} />{t.nonDelivery}</label>{!deliveryConfirmed && <><p className="notice">{t.uncertainDelivery}</p><label className="field">{t.stopReason}<textarea rows={3} required maxLength={500} value={adjustReason} disabled={busy || uncertain} onChange={(event) => setAdjustReason(event.target.value)} /></label></>}{deliveryConfirmed && stopCashHandbackRequired && <label className="cash-handback"><input type="checkbox" checked={cashHandback} disabled={busy || uncertain} onChange={(event) => setCashHandback(event.target.checked)} />{t.cashHandback}</label>}<button className="button primary wide" disabled={busy || uncertain || (!deliveryConfirmed && !adjustReason.trim()) || deliveryConfirmed && stopCashHandbackRequired && !cashHandback} onClick={() => stopAction(stopOrder, stopRequestID, 'finalize', deliveryConfirmed ? '' : adjustReason.trim(), deliveryConfirmed)}>{deliveryConfirmed ? t.stopFinalized : t.uncertainDelivery}</button></>}
      <button className="button quiet wide" disabled={busy || uncertain} onClick={clearStopDialog}>{t.closeDialog}</button>{uncertain && <button className="button retry wide" disabled={busy} onClick={retryWrite}>{t.retry}</button>}{error && <p className="error" role="alert">{error}</p>}</StaffDialog>}
    {dialog === 'adjust' && adjustOrder && <StaffDialog labelledBy="adjust-title" dismissible={!busy && !uncertain} onDismiss={() => { invalidateAdjustmentPreview(); setDialog(null); }}><div className="eyebrow">{t.orders}</div><h2 id="adjust-title">{t.adjustTitle}</h2><p>{adjustOrder.table_name} · #{adjustOrder.generation} · {amountJPY(adjustOrder.effective_total_gross_jpy ?? adjustOrder.total_gross_jpy)}</p>
      <ul className="adjustment-lines">{adjustOrder.lines.map((line, index) => <li key={`${line.product_name}-${index}`}><span>{line.product_name} · {line.quantity}</span><label className="field">{locale === 'ja' ? '取消数量' : '取消數量'}<input type="number" min={0} max={line.quantity} step={1} inputMode="numeric" value={cancelQty[index] ?? 0} disabled={busy || uncertain} onChange={(event) => { const value = Math.max(0, Math.min(line.quantity, Number(event.target.value) || 0)); invalidateAdjustmentPreview(); setCancelQty((current) => current.map((qty, i) => i === index ? value : qty)); }} /></label></li>)}</ul>
      <label className="field">{t.adjustReason}<textarea rows={3} required maxLength={500} value={adjustReason} disabled={busy || uncertain} onChange={(event) => { invalidateAdjustmentPreview(); setAdjustReason(event.target.value); }} /></label>
      {adjustOrder.payment_method === 'CASH' && adjustOrder.payment_status === 'PAID' && <label className="cash-handback"><input type="checkbox" checked={cashHandback} disabled={busy || uncertain} onChange={(event) => { invalidateAdjustmentPreview(); setCashHandback(event.target.checked); }} />{t.cashHandback}</label>}
      <button className="button quiet wide" disabled={busy || uncertain || adjustPreviewBusy || !adjustReason.trim() || !cancelQty.some((qty) => qty > 0)} onClick={() => void previewOrderAdjustment()}>{t.previewAdjustment}</button>
      {adjustPreview && <div className="adjustment-preview" role="status"><p>{t.adjustedTotal} <strong>{amountJPY(adjustPreview.effective_total_gross_jpy)}</strong></p><p>{t.refundAmount} <strong>{amountJPY(adjustPreview.refund_amount_jpy)}</strong></p>{adjustPreview.whole_order_cancel && <p className="notice">{t.cancelled}</p>}</div>}
      <button className="button primary wide" disabled={busy || uncertain || adjustPreviewBusy || !adjustPreview || adjustPreview.expected_version !== adjustOrder.version || adjustOrder.payment_method === 'CASH' && adjustOrder.payment_status === 'PAID' && !cashHandback} onClick={confirmOrderAdjustment}>{t.confirmAdjustment}</button><button className="button quiet wide" disabled={busy || uncertain} onClick={() => { invalidateAdjustmentPreview(); setDialog(null); }}>{t.closeDialog}</button>{uncertain && <button className="button retry wide" disabled={busy} onClick={retryWrite}>{t.retry}</button>}{error && <p className="error" role="alert">{error}</p>}</StaffDialog>}
  </StaffShell>;
}

function StaffOrderCard({ order, locale, t, canServe, busy, compact = false, demo = false, paymentStatus, paymentBusy = false, paymentError = '', canCheckPayment = false, canManage = false, onCheckPayment, onReconcilePayment, onVoid, onStopRequest, onStopAction, onAdjust, onRetryRefund, onAcknowledgeUpdate, onAccept, onStart, onReady, onServe }: {
  order: StaffOrder; locale: Locale; t: typeof copy[Locale]; canServe: boolean; busy: boolean; compact?: boolean; demo?: boolean;
  paymentStatus?: CheckoutStatus; paymentBusy?: boolean; paymentError?: string; canCheckPayment?: boolean; canManage?: boolean;
  onCheckPayment?: () => void; onReconcilePayment?: () => void; onVoid?: (button: HTMLButtonElement) => void; onStopRequest?: (button: HTMLButtonElement) => void; onStopAction?: (requestID: string, action: 'withdraw' | 'confirm' | 'reject' | 'finalize', button: HTMLButtonElement) => void;
  onAdjust?: () => void; onRetryRefund?: (refundID: string) => void; onAcknowledgeUpdate?: () => void;
  onAccept?: () => void; onStart?: () => void; onReady?: () => void; onServe?: () => void;
}) {
  const activeState = order.fulfillment_status;
  return <article className={`staff-order-card ${compact ? 'compact' : ''}`}>
    <header className="staff-order-head"><div><strong>{order.table_name} · #{order.generation}</strong><small>{new Date(order.created_at).toLocaleTimeString(locale === 'ja' ? 'ja-JP' : 'zh-TW', { hour: '2-digit', minute: '2-digit' })} · {order.id}</small></div><span className={`status-chip status-${activeState.toLowerCase()}`}>{statusLabel(activeState, t)}</span></header>
    {order.requires_staff_acceptance && !order.staff_accepted && <p className="notice">{t.waiting}</p>}
    <ul className="staff-order-lines">{order.lines.map((line, index) => <li key={`${line.product_name}-${index}`}><span>{line.product_name} × {line.quantity}{line.options.map((option, optionIndex) => <small key={`${option.group_name}-${option.option_name}-${optionIndex}`}>{option.group_name.trim() ? `${option.group_name}: ` : ''}{option.option_name}</small>)}</span>{!compact && <strong>{amountJPY(line.line_gross_jpy)}</strong>}</li>)}</ul>
    <StopRequests order={order} t={t} compact={compact} canManage={canManage} busy={busy} onAction={onStopAction} />
    {order.order_updated && <p className="notice" role="status">{t.orderUpdated}</p>}
    {!compact && <div className="staff-order-payment"><span>{order.payment_method === 'CASH' && demo ? t.demoCash : order.payment_method === 'CASH' ? t.cash : t.stripe}</span><span>{paymentLabel(order.payment_status, t)}</span><strong>{amountJPY(order.effective_total_gross_jpy ?? order.total_gross_jpy)}</strong></div>}
    {!compact && order.refunds?.length ? <section className="refund-list"><strong>{t.refunds}</strong>{order.refunds.map((refund) => <div key={refund.id}><span>{amountJPY(refund.amount_jpy)} · {refund.status === 'SUCCEEDED' ? t.refundSucceeded : refund.status === 'FAILED' ? t.refundFailed : refund.safe_error_code === 'REFUND_DISPATCH_PENDING' ? t.refundDispatchPending : refund.safe_error_code === 'HOLD_RECONCILIATION_NEEDED' ? t.refundReconciliationNeeded : t.refundProcessing}</span>{canManage && refund.status === 'FAILED' && onRetryRefund && <button className="button small retry" disabled={busy} onClick={() => onRetryRefund(refund.id)}>{t.retryRefund}</button>}</div>)}</section> : null}
    {!compact && order.payment_method === 'STRIPE' && canCheckPayment && <div className="staff-payment-status">
      <button className="button small quiet" disabled={busy || paymentBusy} onClick={onCheckPayment}>{t.paymentCheck}</button>
      {paymentBusy && <p className="muted" role="status">{t.loading}</p>}
      {paymentStatus && <p className="guest-order-status" role="status">{checkoutStateLabel(paymentStatus.state, locale)}</p>}
      {paymentStatus?.payment_review_required && <p className="notice">{t.paymentReview}</p>}
      {paymentStatus?.last_reconciled_at && <small>{t.lastPaymentCheck}: {new Date(paymentStatus.last_reconciled_at).toLocaleString(locale === 'ja' ? 'ja-JP' : 'zh-TW')}</small>}
      {paymentError && <p className="error" role="alert">{paymentError}</p>}
      {paymentStatus && <button className="button small primary" disabled={busy || paymentBusy} onClick={onReconcilePayment}>{paymentBusy ? t.loading : t.paymentReconcile}</button>}
    </div>}
    <div className="staff-order-actions">
      {!compact && canManage && order.payment_method === 'STRIPE' && (order.payment_status === 'PENDING' || order.payment_status === 'UNKNOWN') && order.fulfillment_status === 'WAITING' && onVoid && <button className="button small quiet" disabled={busy} onClick={(event) => onVoid(event.currentTarget)}>{t.voidUnpaid}</button>}
      {!compact && canManage && (order.review_decision ?? 'UNRESOLVED') === 'UNRESOLVED' && (activeState === 'WAITING' || activeState === 'ACCEPTED') && (order.payment_method === 'CASH' || order.payment_status === 'PAID') && onAdjust && <button className="button small quiet" disabled={busy} onClick={onAdjust}>{t.adjust}</button>}
      {!compact && canManage && activeState === 'PREPARING' && onStopRequest && order.lines.some((line) => line.stop_request_eligibility === 'ELIGIBLE') && <button className="button small quiet" disabled={busy} onClick={(event) => onStopRequest(event.currentTarget)}>{t.stopRequest}</button>}
      {compact && order.order_updated && onAcknowledgeUpdate && <button className="button small primary" disabled={busy} onClick={onAcknowledgeUpdate}>{t.acknowledgeUpdate}</button>}
      {order.requires_staff_acceptance && !order.staff_accepted && canServe && onAccept && <button className="button small primary" disabled={busy} onClick={onAccept}>{t.receive}</button>}
      {compact && order.fulfillment_status === 'ACCEPTED' && !order.order_updated && onStart && <button className="button small primary" disabled={busy} onClick={onStart}>{t.start}</button>}
      {compact && order.fulfillment_status === 'PREPARING' && onReady && <button className="button small primary" disabled={busy} onClick={onReady}>{t.markReady}</button>}
      {!compact && canServe && order.fulfillment_status === 'READY' && onServe && <button className="button small primary" disabled={busy} onClick={onServe}>{t.markServed}</button>}
    </div>
  </article>;
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

function StopRequests({ order, t, compact, canManage, busy = false, onAction }: { order: StaffOrder; t: typeof copy[Locale]; compact: boolean; canManage: boolean; busy?: boolean; onAction?: (requestID: string, action: 'withdraw' | 'confirm' | 'reject' | 'finalize', button: HTMLButtonElement) => void }) {
  const requests = order.cancellation_requests ?? [];
  if (!requests.length) return null;
  return <section className="stop-request-list" aria-label={t.stopRequestPending}>{requests.map((request) => {
    const line = order.lines.find((candidate, index) => (candidate.line_index ?? index) === request.line_index);
    const label = request.status === 'REQUESTED' ? t.stopPending : request.status === 'KITCHEN_CONFIRMED' ? t.stopConfirmed : request.status === 'REJECTED' ? t.stopRejected : request.status === 'WITHDRAWN' ? t.stopWithdrawn : t.stopFinalized;
    return <div className="notice" key={request.id}><strong>{label}</strong><span> · {line?.product_name ?? `#${request.line_index + 1}`} × {request.quantity} · {request.reason}</span>{request.manual_exception_reason && <p>{t.stopManual}: {request.manual_exception_reason}</p>}<div className="staff-order-actions">{compact && request.status === 'REQUESTED' && <><button className="button small primary" disabled={busy} onClick={(event) => onAction?.(request.id, 'confirm', event.currentTarget)}>{t.stopKitchenConfirm}</button><button className="button small quiet" disabled={busy} onClick={(event) => onAction?.(request.id, 'reject', event.currentTarget)}>{t.stopKitchenReject}</button></>}{!compact && canManage && (request.status === 'REQUESTED' || request.status === 'KITCHEN_CONFIRMED') && <button className="button small quiet" disabled={busy} onClick={(event) => onAction?.(request.id, 'withdraw', event.currentTarget)}>{t.stopWithdraw}</button>}{!compact && canManage && request.status === 'KITCHEN_CONFIRMED' && !request.manual_exception_reason && <button className="button small primary" disabled={busy} onClick={(event) => onAction?.(request.id, 'finalize', event.currentTarget)}>{t.stopFinalized}</button>}</div></div>;
  })}</section>;
}

function ReceiptCard({ receipt, t, demo = false, locale = 'ja' }: { receipt: Receipt; t: typeof copy[Locale]; demo?: boolean; locale?: Locale }) {
  return <div className="receipt-card">{demo && <><DemoBanner demo locale={locale} /><p role="note">{t.demoReceipt}</p></>}<div><span>{t.receiptID}</span><strong>{receipt.id}</strong></div><div><span>{t.due}</span><strong>{amountJPY(receipt.due_jpy)}</strong></div><div><span>{t.tax}</span><strong>{amountJPY(receipt.tax.tax_jpy)}</strong></div><div><span>{t.tendered}</span><strong>{amountJPY(receipt.tendered_jpy)}</strong></div><div><span>{t.change}</span><strong>{amountJPY(receipt.change_jpy)}</strong></div><div><span>{t.cashier}</span><strong>{receipt.cashier_id}</strong></div><small>{new Date(receipt.created_at).toLocaleString()}</small></div>;
}

function StaffShell({ children, demo = false, locale, setLocale, t }: { children: React.ReactNode; demo?: boolean; locale: Locale; setLocale: (locale: Locale) => void; t: typeof copy[Locale] }) {
  return <main className="app-shell"><header className="topbar"><a className="brand" href="/staff" aria-label="Tojima staff"><span className="brand-mark">東</span><span><strong>{t.brand}</strong><small>{t.subtitle}</small></span></a><button className="locale-switch" onClick={() => setLocale(locale === 'ja' ? 'zh' : 'ja')}>{locale === 'ja' ? t.zh : t.ja} <span>⌄</span></button></header><DemoBanner demo={demo} locale={locale} />{children}</main>;
}

function explain(error: unknown, locale: Locale) {
  if (error instanceof ApiError) {
    if (error.status === 401 || error.status === 403) return locale === 'ja' ? 'この操作を行う権限がありません。' : '您沒有執行此操作的權限。';
    if (error.status === 410) return locale === 'ja' ? 'この卓セッションは終了しています。最新状態を更新してください。' : '此桌次已結束，請重新整理最新狀態。';
    if (error.status === 409 && error.body.error === 'close_blocked') return locale === 'ja' ? '未精算または未提供の注文があるため閉卓できません。' : copy.zh.closeBlocked;
    if (error.body.error === 'stop_request_pending') return locale === 'ja' ? '停止依頼が未解決です。厨房の確認または店長の対応をお待ちください。' : '停止申請尚未處理，請等待廚房確認或主管處理。';
    if (error.body.error === 'stop_request_state') return locale === 'ja' ? '停止依頼の状態が変わりました。最新状態を確認してください。' : '停止申請狀態已變更，請確認最新狀態。';
    if (error.body.error === 'manual_exception_required') return locale === 'ja' ? '提供状況が不明なため手動対応が必要です。理由を記録してください。' : '送達狀態不明，需記錄原因並轉交人工處理。';
  }
  return error instanceof ApiError && error.status < 500
    ? (locale === 'ja' ? '操作を完了できませんでした。最新状態を確認してください。' : '操作無法完成，請確認最新狀態。')
    : copy[locale].network;
}
function statusLabel(status: StaffOrder['fulfillment_status'] | StaffTable['status'], t: typeof copy[Locale]) {
  if (!status) return t.unknown;
  if (status === 'OPEN') return t.open; if (status === 'CLOSED') return t.closed; if (status === 'IDLE') return t.idle;
  if (status === 'ACCEPTED') return t.accepted; if (status === 'PREPARING') return t.preparing; if (status === 'READY') return t.ready;
  if (status === 'SERVED') return t.served; if (status === 'WAITING') return t.waiting; if (status === 'CANCELLED') return t.cancelled;
  return status;
}
function paymentLabel(status: StaffOrder['payment_status'], t: typeof copy[Locale]) {
  if (status === 'PAID') return t.paid; if (status === 'REFUNDED') return t.refunded; if (status === 'REFUND_PENDING') return t.refundPending; if (status === 'VOIDED') return t.voided; if (status === 'UNPAID') return t.unpaidStatus; if (status === 'PENDING') return t.pending; return t.unknown;
}
function stopNeedsCashHandback(order: StaffOrder | null, requestID: string) {
  if (!order || order.payment_method !== 'CASH' || order.payment_status !== 'PAID') return false;
  const request = order.cancellation_requests?.find((item) => item.id === requestID);
  if (!request || request.quantity <= 0) return false;
  const line = order.lines.find((item, index) => (item.line_index ?? index) === request.line_index);
  // Refund sign matches calculateAdjustmentMode's quantity × historical unit gross, including options.
  return line !== undefined && Number(line.unit_gross_jpy) > 0;
}
function modeLabel(mode: string, locale: Locale) {
  if (mode === 'DIRECT_SCAN') return locale === 'ja' ? 'QR参加' : '掃碼加入';
  if (mode === 'SESSION_PIN') return locale === 'ja' ? 'PIN参加' : 'PIN 加入';
  if (mode === 'STAFF_OPEN') return locale === 'ja' ? 'スタッフ開卓' : '店員開桌';
  return locale === 'ja' ? '卓セッション' : '桌次';
}
function tableStatus(status: StaffTable['status'], t: typeof copy[Locale]) { return statusLabel(status, t); }
function blockerLabel(value: string, locale: Locale) {
  const lower = value.toLowerCase();
  if (lower.includes('stop_request_pending')) return locale === 'ja' ? copy.ja.stopRequestPending : copy.zh.stopRequestPending;
  if (lower.includes('stop_manual_exception') || lower.includes('manual_exception')) return locale === 'ja' ? copy.ja.stopManual : copy.zh.stopManual;
  if (lower.includes('operation_hold') || lower.includes('hold')) return locale === 'ja' ? '運営保留中のため、責任者の確認が必要です。' : '此桌次目前由營運保留，請由負責人確認後再關桌。';
  if (lower.includes('cash')) return locale === 'ja' ? '未精算の現金注文があります。' : '仍有未結清的現金訂單。';
  if (lower.includes('unpaid') || lower.includes('payment') || lower.includes('stripe') || lower.includes('refund')) return locale === 'ja' ? '未確認または未精算の支払いがあります。' : '仍有付款未確認或未結清。';
  if (lower.includes('served') || lower.includes('fulfillment') || lower.includes('kitchen')) return locale === 'ja' ? 'まだ提供していない注文があります。' : '仍有訂單尚未送達。';
  return locale === 'ja' ? '注文の状態を確認してください。' : '請確認訂單狀態。';
}
function tenderedEnough(tendered: string, due: string) {
  if (!/^\d+$/.test(tendered) || !/^\d+$/.test(due)) return false;
  try { return BigInt(tendered) >= BigInt(due); } catch { return false; }
}
