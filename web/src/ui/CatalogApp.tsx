import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, amountJPY } from '../api/client';
import { useFieldArray, useForm, type Control, type UseFormRegister } from 'react-hook-form';
import {
  catalogClient,
  toNumericDraft,
} from '../api/catalog';
import styles from './CatalogApp.module.css';
import { DemoBanner } from './DemoBanner';
import type { DemoSurfaceProps } from './DemoBanner';
import type {
  Availability,
  CatalogCategory,
  CatalogContext,
  CatalogData,
  CatalogProduct,
  CompanyDraft,
  CompanyPreview,
  CompanyPublished,
  PublicationIssue,
  StoreCatalog,
} from '../api/catalog';

type Locale = 'ja' | 'zh';
type Panel = 'company' | 'store';
const SESSION_KEY = 'tojima.catalog.fixture-context';

const copy = {
  ja: {
    brand: '東島', title: 'メニュー管理', subtitle: '本部と店舗のメニュー', tenant: '企業ID', store: '店舗ID', token: 'テスト用アクセストークン', connect: '接続', disconnect: '切断',
    company: '本部メニュー', storeMenu: '店舗メニュー', published: '公開中', draft: '編集中', revision: '公開版', currentPublished: '現在公開中の内容を表示', draftVersion: '草稿', linkedCount: '連動中の店舗', refresh: '最新状態に更新',
    addCategory: 'カテゴリを追加', addProduct: '商品を追加', addGroup: '選択肢グループを追加', addOption: '選択肢を追加', remove: '削除', categoryName: 'カテゴリ名', productName: '商品名', description: '説明', basePrice: '基本価格', optionGroup: '選択肢グループ', groupName: 'グループ名', min: '必須の最小数', max: '選択できる最大数', optionName: '選択肢名', extraPrice: '追加料金', active: '販売対象',
    saveDraft: '草稿を保存', preview: '公開前チェック', publish: '確認した内容を公開', stale: '内容が更新されています。最新情報を読み込み、もう一度確認してください。', unsaved: '未保存の変更があります。保存後に公開前チェックを行ってください。', previewNeeded: '公開前チェックを行い、問題がないことを確認してください。', previewValid: '公開可能です。確認した内容を公開できます。', previewBlocked: '修正が必要な項目があります。修正後にもう一度チェックしてください。', noIssues: '公開前チェックに問題はありません。',
    newProductPending: '新しい商品と選択肢は、連動中の各店舗で確認されるまで販売できません。', issueNewGroup: '販売中の商品に新しい必須グループを追加できません。', issueOptionalRequired: '販売中の商品で任意グループを必須に変更できません。', issueMinIncrease: '必須選択数を増やすと既存店舗で注文できなくなる可能性があります。', issueChoiceShortage: 'この店舗では販売中の必須選択肢が不足します。', issueFallback: '公開内容の確認が必要です。',
    changeSummaryTitle: '公開内容の変更概要', changeSummaryAdvisory: 'この概要は草稿と公開版の差分です。公開可否は下記のサーバー検証結果を確認してください。', noCatalogChanges: '公開版からの変更はありません。',
    addedCategory: '追加カテゴリ', removedCategory: '削除カテゴリ', addedProduct: '追加商品', removedProduct: '削除商品', addedGroup: '追加選択肢グループ', removedGroup: '削除選択肢グループ', addedOption: '追加選択肢', removedOption: '削除選択肢', changedText: '名称・説明の変更', changedPrice: '価格の変更', changedRequiredRule: '必須選択ルールの変更', changedSalesStatus: '販売対象の変更', moreItems: (count: number) => `ほか${count}件`,
    modeLinked: '本部メニューと連動', modeDetached: 'この店舗の専用メニュー', sourcePrice: '本部基本価格', detachedBasePrice: '店舗メニューの基本価格', overridePrice: 'この店舗の価格', effectivePrice: '現在の販売価格', resetPrice: '本部価格に戻す', savePrice: '店舗価格を保存', availability: '販売状態', pending: '店舗確認待ち', available: '販売中', paused: '一時停止', confirmSale: '販売を確認', pause: '販売を一時停止', reopen: '販売を再開', reason: '停止理由', reasonRequired: '停止理由を入力してください。',
    detach: '専用メニューに切り替える', detachExplain: '現在の公開メニューを複製します。以後、本部の公開はこの店舗に反映されません。', detachConfirm: '複製して切り替える', cancel: 'キャンセル', detachedDraft: '店舗専用メニューの草稿', saveStoreDraft: '店舗草稿を保存', publishStoreDraft: '店舗メニューを公開',
    unauthorized: 'この認証情報では表示できません。許可されたテスト用アクセストークンを確認してください。', network: '通信を完了できませんでした。最新状態を更新してから操作してください。', genericError: '操作を完了できませんでした。最新状態を確認してください。',
    empty: 'メニューがありません。', noCompany: '本部メニューを利用できません。', noStore: 'この店舗のメニューを利用できません。', amountUnsupported: '金額を安全に編集できません。より小さい整数を入力してください。',
    language: '繁體中文', ja: '日本語', zh: '繁體中文', signInHint: 'テスト用認証情報を使用します。実際のログインはこの画面では行いません。', loading: '確認中…', savedCompany: '草稿を保存しました。公開前チェックを実行してください。', savedStore: '店舗草稿を保存しました。公開前に内容を確認してください。', publishDone: 'メニューを公開しました。', detachedDone: '専用メニューに切り替えました。', priceSaved: '店舗価格を保存しました。', availabilitySaved: '販売状態を更新しました。',
  },
  zh: {
    brand: '東島', title: '菜單管理', subtitle: '總部與分店菜單', tenant: '企業 ID', store: '分店 ID', token: '測試用存取權杖', connect: '連線', disconnect: '登出',
    company: '總部菜單', storeMenu: '分店菜單', published: '已發布', draft: '編輯中', revision: '發布版本', currentPublished: '檢視目前已發布內容', draftVersion: '草稿版本', linkedCount: '連動中的分店', refresh: '更新最新狀態',
    addCategory: '新增分類', addProduct: '新增商品', addGroup: '新增選項群組', addOption: '新增選項', remove: '刪除', categoryName: '分類名稱', productName: '商品名稱', description: '說明', basePrice: '基本價格', optionGroup: '選項群組', groupName: '群組名稱', min: '必選最少數量', max: '最多選擇數量', optionName: '選項名稱', extraPrice: '加價金額', active: '納入販售',
    saveDraft: '儲存草稿', preview: '檢查發布內容', publish: '發布已確認內容', stale: '內容已更新。請重新載入最新狀態，再次檢查。', unsaved: '有尚未儲存的變更。儲存後再檢查發布內容。', previewNeeded: '請先檢查發布內容，確認沒有問題。', previewValid: '可以發布。請確認後再發布。', previewBlocked: '有需要修正的項目，修正後請重新檢查。', noIssues: '檢查完成，沒有發現發布問題。',
    newProductPending: '新增商品與選項在連動分店確認前不會開放販售。', issueNewGroup: '已販售商品不能新增必選群組。', issueOptionalRequired: '不能將已販售商品的可選群組改為必選。', issueMinIncrease: '提高必選數量可能導致現有分店無法接單。', issueChoiceShortage: '此分店仍在販售的必選項目不足。', issueFallback: '發布內容需要確認。',
    changeSummaryTitle: '發布內容變更摘要', changeSummaryAdvisory: '此摘要比較草稿與已發布版本。是否可發布仍以以下伺服器檢查結果為準。', noCatalogChanges: '與已發布版本相比沒有變更。',
    addedCategory: '新增分類', removedCategory: '刪除分類', addedProduct: '新增商品', removedProduct: '刪除商品', addedGroup: '新增選項群組', removedGroup: '刪除選項群組', addedOption: '新增選項', removedOption: '刪除選項', changedText: '名稱或說明變更', changedPrice: '價格變更', changedRequiredRule: '必選規則變更', changedSalesStatus: '販售狀態變更', moreItems: (count: number) => `另有 ${count} 項`,
    modeLinked: '連動總部菜單', modeDetached: '分店專用菜單', sourcePrice: '總部基本價格', detachedBasePrice: '分店菜單基本價格', overridePrice: '本店價格', effectivePrice: '目前售價', resetPrice: '恢復總部價格', savePrice: '儲存分店價格', availability: '販售狀態', pending: '待分店確認', available: '販售中', paused: '暫停販售', confirmSale: '確認開放販售', pause: '暫停販售', reopen: '恢復販售', reason: '停售原因', reasonRequired: '請輸入停售原因。',
    detach: '切換為分店專用菜單', detachExplain: '複製目前已發布菜單。之後總部發布內容不會套用至此分店。', detachConfirm: '複製並切換', cancel: '取消', detachedDraft: '分店專用菜單草稿', saveStoreDraft: '儲存分店草稿', publishStoreDraft: '發布分店菜單',
    unauthorized: '此權杖無法檢視此內容。請確認使用已授權的測試用存取權杖。', network: '無法完成連線。請先更新最新狀態，再重新操作。', genericError: '無法完成操作，請確認最新狀態。',
    empty: '目前沒有菜單。', noCompany: '無法使用總部菜單。', noStore: '無法使用此分店菜單。', amountUnsupported: '金額無法安全編輯，請輸入較小的整數。',
    language: '日本語', ja: '日本語', zh: '繁體中文', signInHint: '此處使用測試用認證資訊，不提供正式登入。', loading: '確認中…', savedCompany: '草稿已儲存，請執行發布前檢查。', savedStore: '分店草稿已儲存，請再次確認內容。', publishDone: '菜單已發布。', detachedDone: '已切換為分店專用菜單。', priceSaved: '分店價格已儲存。', availabilitySaved: '販售狀態已更新。',
  },
} as const;

type SameKeys<Left, Right> = Exclude<keyof Left, keyof Right> extends never
  ? Exclude<keyof Right, keyof Left> extends never ? true : false
  : false;
const localeKeysMatch: SameKeys<typeof copy.ja, typeof copy.zh> = true;
void localeKeysMatch;

interface StoredContext extends CatalogContext { token: string }

export function CatalogApp({ demo = false }: DemoSurfaceProps) {
  const [locale, setLocale] = useState<Locale>('ja');
  const t = copy[locale];
  const [tenantID, setTenantID] = useState('');
  const [storeID, setStoreID] = useState('');
  const [token, setToken] = useState('');
  const [context, setContext] = useState<StoredContext | null>(null);
  const [panel, setPanel] = useState<Panel>('company');
  const [busy, setBusy] = useState(false);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [companyError, setCompanyError] = useState('');
  const [storeError, setStoreError] = useState('');
  const [published, setPublished] = useState<CompanyPublished | null>(null);
  const [companySaved, setCompanySaved] = useState<CompanyDraft | null>(null);
  const [companyDraft, setCompanyDraft] = useState<CompanyDraft | null>(null);
  const [companyFormEpoch, setCompanyFormEpoch] = useState(0);
  const [preview, setPreview] = useState<CompanyPreview | null>(null);
  const [storeMenu, setStoreMenu] = useState<StoreCatalog | null>(null);
  const [storeSaved, setStoreSaved] = useState<CompanyDraft | null>(null);
  const [storeDraft, setStoreDraft] = useState<CompanyDraft | null>(null);
  const [storeFormEpoch, setStoreFormEpoch] = useState(0);
  const [priceInputs, setPriceInputs] = useState<Record<string, string>>({});
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const [pauseTarget, setPauseTarget] = useState('');
  const [confirmDetach, setConfirmDetach] = useState(false);
  const epoch = useRef(0);
  const previewEpoch = useRef(0);
  const autoRestored = useRef(false);

  const companyDirty = !!companyDraft && !!companySaved && !sameCatalog(companyDraft, companySaved);
  const storeDirty = !!storeDraft && !!storeSaved && !sameCatalog(storeDraft, storeSaved);
  const companyCanSave = !!companyDraft && companyDirty && canWriteDraft(companyDraft);
  const storeCanSave = !!storeDraft && storeDirty && canWriteDraft(storeDraft);
  const currentPreview = !!preview && !!published && !!companyDraft && !!companySaved && !companyDirty
    && preview.draft_version === companyDraft.version && preview.draft_version === companySaved.version
    && preview.published_revision === published.revision ? preview : null;
  const resetData = useCallback(() => {
    previewEpoch.current++;
    setPreviewBusy(false);
    setPublished(null); setCompanySaved(null); setCompanyDraft(null); setPreview(null);
    setStoreMenu(null); setStoreSaved(null); setStoreDraft(null); setPriceInputs({}); setReasons({}); setPauseTarget(''); setConfirmDetach(false);
    setCompanyError(''); setStoreError(''); setNotice('');
  }, []);

  // This is an explicit snapshot editor, not a read/poll screen; refreshes stay user- or mutation-triggered.
  const loadCompany = useCallback(async (current: StoredContext, requestEpoch: number) => {
    setCompanyError('');
    const base = { tenantID: current.tenantID, token: current.token };
    const [publishedResult, draftResult] = await Promise.allSettled([
      catalogClient.companyPublished(base.tenantID, base.token),
      catalogClient.companyDraft(base.tenantID, base.token),
    ]);
    if (epoch.current !== requestEpoch) return false;
    setCompanyFormEpoch((value) => value + 1);
    if (publishedResult.status === 'fulfilled') setPublished(publishedResult.value);
    else if (publishedResult.reason instanceof ApiError && publishedResult.reason.status === 404) setPublished({ revision: 0, categories: [] });
    else setPublished(null);
    if (draftResult.status === 'fulfilled') {
      setCompanySaved(draftResult.value); setCompanyDraft(draftResult.value);
    } else { setCompanySaved(null); setCompanyDraft(null); }
    setPreview(null);
    const publishedMissing = publishedResult.status === 'rejected' && publishedResult.reason instanceof ApiError && publishedResult.reason.status === 404;
    const failure = publishedMissing ? draftResult.status === 'rejected' ? draftResult.reason : null : publishedResult.status === 'rejected' ? publishedResult.reason : draftResult.status === 'rejected' ? draftResult.reason : null;
    if (failure) setCompanyError(errorCopy(failure, locale));
    return !failure;
  }, [locale]);

  const loadStore = useCallback(async (current: StoredContext, requestEpoch: number) => {
    setStoreError('');
    try {
      const menu = await catalogClient.storeMenu(current, current.token);
      if (epoch.current !== requestEpoch) return false;
      setStoreMenu(menu);
      setPriceInputs(Object.fromEntries(menu.categories.flatMap((category) => category.products.map((product) => [product.key, String(product.override_active ? product.override_price_minor ?? '' : product.base_price_minor)]))));
      if (menu.mode === 'DETACHED' && menu.can_confirm_availability) {
        const draft = await catalogClient.storeDraft(current, current.token);
        if (epoch.current !== requestEpoch) return false;
        setStoreSaved(draft); setStoreDraft(draft);
      } else { setStoreSaved(null); setStoreDraft(null); }
      setStoreFormEpoch((value) => value + 1);
      return true;
    } catch (cause) {
      if (epoch.current !== requestEpoch) return false;
      setStoreMenu(null); setStoreSaved(null); setStoreDraft(null); setPriceInputs({}); setStoreFormEpoch((value) => value + 1);
      setStoreError(errorCopy(cause, locale));
      return false;
    }
  }, [locale]);

  const refresh = useCallback(async () => {
    if (!context) return;
    const requestEpoch = epoch.current;
    previewEpoch.current++; setPreview(null); setBusy(true); setNotice('');
    const results = await Promise.all([
      loadCompany(context, requestEpoch),
      loadStore(context, requestEpoch),
    ]);
    if (epoch.current === requestEpoch) {
      if (results[0]) setPanel('company');
      else if (results[1]) setPanel('store');
      setBusy(false);
    }
  }, [context, loadCompany, loadStore]);

  const connect = useCallback(async (input: StoredContext) => {
    const cleaned = { tenantID: input.tenantID.trim(), storeID: input.storeID.trim(), token: input.token.trim() };
    if (!cleaned.tenantID || !cleaned.storeID || !cleaned.token) return;
    const requestEpoch = ++epoch.current;
    resetData(); setBusy(true); setContext(cleaned); setTenantID(cleaned.tenantID); setStoreID(cleaned.storeID); setToken(cleaned.token);
    sessionStorage.setItem(SESSION_KEY, JSON.stringify(cleaned));
    const [companyOK, storeOK] = await Promise.all([loadCompany(cleaned, requestEpoch), loadStore(cleaned, requestEpoch)]);
    if (epoch.current === requestEpoch) {
      setPanel(companyOK ? 'company' : 'store');
      if (!companyOK && !storeOK) setNotice(locale === 'ja' ? '認証情報と企業・店舗IDを確認してください。' : '請確認認證資訊及企業／分店 ID。');
      setBusy(false);
    }
  }, [loadCompany, loadStore, locale, resetData]);

  const disconnect = () => {
    epoch.current++; resetData(); setContext(null); setTenantID(''); setStoreID(''); setToken(''); setBusy(false);
    sessionStorage.removeItem(SESSION_KEY);
  };

  useEffect(() => {
    if (autoRestored.current) return;
    autoRestored.current = true;
    try {
      const saved = sessionStorage.getItem(SESSION_KEY);
      if (saved) {
        const parsed = JSON.parse(saved) as StoredContext;
        setTenantID(parsed.tenantID); setStoreID(parsed.storeID); setToken(parsed.token);
        if (parsed.tenantID && parsed.storeID && parsed.token) void connect(parsed);
      }
    } catch { sessionStorage.removeItem(SESSION_KEY); }
  }, [connect]);

  useEffect(() => { document.documentElement.lang = locale === 'ja' ? 'ja' : 'zh-Hant'; }, [locale]);

  const editCompany = (data: CatalogData) => {
    if (!companyDraft) return;
    previewEpoch.current++; setPreview(null); setCompanyDraft({ ...data, version: companyDraft.version }); setCompanyError(''); setNotice('');
  };
  const editStoreDraft = (data: CatalogData) => {
    if (!storeDraft) return;
    setStoreDraft({ ...data, version: storeDraft.version }); setNotice('');
  };

  const saveCompany = async () => {
    if (!context || !companyDraft || !canWriteDraft(companyDraft)) { setCompanyError(t.amountUnsupported); return; }
    const requestEpoch = epoch.current; setBusy(true); setNotice(''); setPreview(null);
    try {
      await catalogClient.saveCompanyDraft(context.tenantID, context.token, companyDraft);
      const result = await catalogClient.companyDraft(context.tenantID, context.token);
      if (epoch.current !== requestEpoch) return;
      setCompanyDraft(result); setCompanySaved(result); setNotice(t.savedCompany);
      setCompanyFormEpoch((value) => value + 1);
    } catch (cause) {
      if (epoch.current !== requestEpoch) return;
      if (isConflict(cause)) { await loadCompany(context, requestEpoch); setCompanyError(t.stale); }
      else setCompanyError(errorCopy(cause, locale));
    } finally { if (epoch.current === requestEpoch) setBusy(false); }
  };

  const runPreview = async () => {
    if (!context || !companyDraft || companyDirty) return;
    const requestEpoch = epoch.current; const previewRequest = ++previewEpoch.current;
    setPreviewBusy(true); setPreview(null); setCompanyError(''); setNotice('');
    try {
      const result = await catalogClient.previewCompany(context.tenantID, context.token, companyDraft.version);
      if (epoch.current !== requestEpoch || previewEpoch.current !== previewRequest) return;
      setPreview(result);
      setNotice(result.can_publish ? t.previewValid : t.previewBlocked);
    } catch (cause) {
      if (epoch.current !== requestEpoch || previewEpoch.current !== previewRequest) return;
      if (isConflict(cause)) { await loadCompany(context, requestEpoch); setCompanyError(t.stale); }
      else setCompanyError(errorCopy(cause, locale));
    } finally { if (epoch.current === requestEpoch) setPreviewBusy(false); }
  };

  const publishCompany = async () => {
    if (!context || !companyDraft || !preview || companyDirty || !preview.can_publish || preview.draft_version !== companyDraft.version) return;
    const requestEpoch = epoch.current; setBusy(true); setCompanyError(''); setNotice('');
    try {
      await catalogClient.publishCompany(context.tenantID, context.token, preview.draft_version, preview.preview_fingerprint);
      if (epoch.current !== requestEpoch) return;
      setPreview(null); setNotice(t.publishDone);
      await Promise.all([loadCompany(context, requestEpoch), loadStore(context, requestEpoch)]);
    } catch (cause) {
      if (epoch.current !== requestEpoch) return;
      if (isConflict(cause)) { setPreview(null); await Promise.all([loadCompany(context, requestEpoch), loadStore(context, requestEpoch)]); setCompanyError(t.stale); }
      else setCompanyError(errorCopy(cause, locale));
    } finally { if (epoch.current === requestEpoch) setBusy(false); }
  };

  const saveDetachedDraft = async () => {
    if (!context || !storeDraft || !canWriteDraft(storeDraft)) { setStoreError(t.amountUnsupported); return; }
    const requestEpoch = epoch.current; setBusy(true); setNotice('');
    try {
      await catalogClient.saveStoreDraft(context, context.token, storeDraft);
      const result = await catalogClient.storeDraft(context, context.token);
      if (epoch.current !== requestEpoch) return;
      setStoreDraft(result); setStoreSaved(result); setNotice(t.savedStore);
      setStoreFormEpoch((value) => value + 1);
    } catch (cause) {
      if (epoch.current !== requestEpoch) return;
      if (isConflict(cause)) { await loadStore(context, requestEpoch); setStoreError(t.stale); }
      else setStoreError(errorCopy(cause, locale));
    } finally { if (epoch.current === requestEpoch) setBusy(false); }
  };

  const publishDetachedDraft = async () => {
    if (!context || !storeDraft || storeDirty || !storeSaved) return;
    const requestEpoch = epoch.current; setBusy(true); setNotice('');
    try {
      await catalogClient.publishStoreDraft(context, context.token, storeDraft.version);
      if (epoch.current !== requestEpoch) return;
      setNotice(t.publishDone); await loadStore(context, requestEpoch);
    } catch (cause) {
      if (epoch.current !== requestEpoch) return;
      if (isConflict(cause)) { await loadStore(context, requestEpoch); setStoreError(t.stale); }
      else setStoreError(errorCopy(cause, locale));
    } finally { if (epoch.current === requestEpoch) setBusy(false); }
  };

  const detach = async () => {
    if (!context || !storeMenu?.can_detach) return;
    const requestEpoch = epoch.current; setBusy(true); setConfirmDetach(false); setNotice('');
    try {
      await catalogClient.detachStore(context, context.token, storeMenu.revision);
      if (epoch.current !== requestEpoch) return;
      setNotice(t.detachedDone); await loadStore(context, requestEpoch);
    } catch (cause) {
      if (epoch.current !== requestEpoch) return;
      if (isConflict(cause)) { await loadStore(context, requestEpoch); setStoreError(t.stale); }
      else setStoreError(errorCopy(cause, locale));
    } finally { if (epoch.current === requestEpoch) setBusy(false); }
  };

  const changePrice = async (product: CatalogProduct, reset: boolean) => {
    if (!context || !storeMenu?.can_edit_prices) return;
    const raw = priceInputs[product.key] ?? '';
    if (!reset && (!/^\d+$/.test(raw) || !Number.isSafeInteger(Number(raw)))) { setStoreError(t.amountUnsupported); return; }
    const requestEpoch = epoch.current; setBusy(true); setStoreError(''); setNotice('');
    try {
      if (reset) await catalogClient.resetOverride(context, context.token, product.key, product.price_override_version ?? 0);
      else await catalogClient.setOverride(context, context.token, product.key, raw, product.price_override_version ?? 0);
      if (epoch.current !== requestEpoch) return;
      setNotice(t.priceSaved); await loadStore(context, requestEpoch);
    } catch (cause) {
      if (epoch.current !== requestEpoch) return;
      if (isConflict(cause)) { await loadStore(context, requestEpoch); setStoreError(t.stale); }
      else setStoreError(errorCopy(cause, locale));
    } finally { if (epoch.current === requestEpoch) setBusy(false); }
  };

  const availability = async (targetKey: string, target: { kind: 'PRODUCT' | 'OPTION'; product_key: string; group_key?: string; option_key?: string }, status: Availability, version: number) => {
    if (!context || !storeMenu) return;
    const reason = status === 'PAUSED' ? (reasons[targetKey] ?? '').trim() : '';
    if (status === 'PAUSED' && !reason) { setStoreError(t.reasonRequired); return; }
    const requestEpoch = epoch.current; setBusy(true); setStoreError(''); setNotice('');
    try {
      await catalogClient.setAvailability(context, context.token, { target, status, ...(reason ? { reason } : {}), expected_version: version });
      if (epoch.current !== requestEpoch) return;
      setPauseTarget(''); setNotice(t.availabilitySaved); await loadStore(context, requestEpoch);
    } catch (cause) {
      if (epoch.current !== requestEpoch) return;
      if (isConflict(cause)) { await loadStore(context, requestEpoch); setStoreError(t.stale); }
      else setStoreError(errorCopy(cause, locale));
    } finally { if (epoch.current === requestEpoch) setBusy(false); }
  };

  const hasCompany = !!published && !!companyDraft;
  const hasStore = !!storeMenu;

  return <main className={`app-shell ${styles.catalogScope}`}>
    <header className="topbar"><a className="brand" href="/catalog" aria-label="Tojima catalog"><span className="brand-mark">東</span><span><strong>{t.brand}</strong><small>{t.subtitle}</small></span></a><button type="button" className="locale-switch" onClick={() => setLocale(locale === 'ja' ? 'zh' : 'ja')}>{t.language} <span>⌄</span></button></header>
    <DemoBanner demo={demo} locale={locale} />
    {!context ? <section className="entry-card catalog-entry"><div className="eyebrow">M01 / M02</div><h1>{t.title}</h1><p>{t.signInHint}</p>
      <form onSubmit={(event) => { event.preventDefault(); void connect({ tenantID, storeID, token }); }}>
        <label className="field">{t.tenant}<input autoComplete="organization" value={tenantID} onChange={(event) => setTenantID(event.target.value)} required /></label>
        <label className="field">{t.store}<input value={storeID} onChange={(event) => setStoreID(event.target.value)} required /></label>
        <label className="field">{t.token}<input type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} required /></label>
        <button type="submit" className="button primary wide" disabled={busy}>{busy ? t.loading : t.connect}</button>
      </form>
      {notice && <p className="error" role="alert">{notice}</p>}
    </section> : <>
      <header className="welcome catalog-welcome"><div><div className="eyebrow">{t.title}</div><h1>{context.tenantID}</h1><p>{context.storeID}</p></div><div className="catalog-header-actions"><button type="button" className="button quiet" disabled={busy} onClick={() => void refresh()}>{t.refresh}</button><button type="button" className="text-button" onClick={disconnect}>{t.disconnect}</button></div></header>
      <nav className="tabs catalog-tabs" aria-label={t.title}>
        {hasCompany && <button type="button" className={`tab ${panel === 'company' ? 'active' : ''}`} onClick={() => setPanel('company')}>{t.company}</button>}
        {hasStore && <button type="button" className={`tab ${panel === 'store' ? 'active' : ''}`} onClick={() => setPanel('store')}>{t.storeMenu}</button>}
        {!hasCompany && !hasStore && <span className="catalog-access-error" role="alert">{companyError || storeError || t.unauthorized}</span>}
      </nav>
      {notice && <p className="notice catalog-notice" role="status">{notice}</p>}
      {panel === 'company' && hasCompany && companyDraft && <section className="catalog-panel" aria-label={t.company}>
        <div className="catalog-summary"><span>{t.revision}: <strong>{published?.revision}</strong></span><span>{t.draftVersion}: <strong>{companyDraft.version}</strong></span>{preview && <span>{t.linkedCount}: <strong>{preview.linked_store_count}</strong></span>}</div>
        {published && <details className="catalog-published-snapshot"><summary>{t.currentPublished}</summary>{published.categories.length === 0 ? <p className="muted">{t.empty}</p> : published.categories.map((category) => <section key={category.key}><h3>{category.name}</h3><ul>{category.products.map((product) => <li key={product.key}><span>{product.name}</span><strong>{amountJPY(product.base_price_minor)}</strong>{product.option_groups.length > 0 && <ul>{product.option_groups.map((group) => <li key={group.key}>{group.name}: {group.options.map((option) => `${option.name} (+${amountJPY(option.price_delta_minor)})`).join('、')}</li>)}</ul>}</li>)}</ul></section>)}</details>}
        <p className="muted">{t.newProductPending}</p>
        <CatalogEditor key={`company-${companyDraft.version}-${companyFormEpoch}`} data={companyDraft} disabled={busy} onChange={editCompany} t={t} />
        {companyDirty && !companyCanSave && <p className="error" role="alert">{t.amountUnsupported}</p>}
        <div className="catalog-actions">
          <button type="button" className="button quiet" disabled={!companyCanSave || busy} onClick={() => void saveCompany()}>{t.saveDraft}</button>
          <button type="button" className="button quiet" disabled={companyDirty || busy || previewBusy} onClick={() => void runPreview()}>{previewBusy ? t.loading : t.preview}</button>
          <button type="button" className="button primary" disabled={!currentPreview || !currentPreview.can_publish || busy || previewBusy} onClick={() => void publishCompany()}>{t.publish}</button>
        </div>
        {companyError && <p className="error" role="alert">{companyError}</p>}
        {companyDirty && <p className="muted">{t.unsaved}</p>}
        {currentPreview && companySaved && <PreviewPanel preview={currentPreview} candidate={companySaved} published={published} t={t} />}
      </section>}
      {panel === 'store' && hasStore && storeMenu && <section className="catalog-panel" aria-label={t.storeMenu}>
        <div className="catalog-summary"><span>{storeMenu.mode === 'LINKED' ? t.modeLinked : t.modeDetached}</span><span>{t.revision}: <strong>{storeMenu.revision}</strong></span></div>
        <StoreCatalogView menu={storeMenu} priceInputs={priceInputs} setPriceInputs={setPriceInputs} reasons={reasons} setReasons={setReasons} pauseTarget={pauseTarget} setPauseTarget={setPauseTarget} busy={busy} onPrice={changePrice} onAvailability={availability} t={t} />
        {storeMenu.mode === 'DETACHED' && storeDraft && storeMenu.can_confirm_availability && <>
          <h2>{t.detachedDraft}</h2>
          <CatalogEditor key={`store-${storeDraft.version}-${storeFormEpoch}`} data={storeDraft} disabled={busy} onChange={editStoreDraft} t={t} />
          <div className="catalog-actions"><button type="button" className="button quiet" disabled={!storeCanSave || busy} onClick={() => void saveDetachedDraft()}>{t.saveStoreDraft}</button><button type="button" className="button primary" disabled={!storeSaved || storeDirty || busy} onClick={() => void publishDetachedDraft()}>{t.publishStoreDraft}</button></div>
        </>}
        {storeMenu.mode === 'LINKED' && storeMenu.can_detach && <section className="catalog-detach"><h2>{t.detach}</h2><p>{t.detachExplain}</p>{!confirmDetach ? <button type="button" className="button quiet" disabled={busy} onClick={() => setConfirmDetach(true)}>{t.detach}</button> : <div className="confirm-dialog" role="group" aria-label={t.detach}><p>{t.detachExplain}</p><button type="button" className="button quiet" disabled={busy} onClick={() => setConfirmDetach(false)}>{t.cancel}</button><button type="button" className="button primary" disabled={busy} onClick={() => void detach()}>{t.detachConfirm}</button></div>}</section>}
        {storeError && <p className="error" role="alert">{storeError}</p>}
      </section>}
    </>}
    <footer className="workspace-footer">東島 <span>·</span> {t.title}</footer>
  </main>;
}

function CatalogEditor({ data, onChange, disabled, t }: { data: CatalogData; onChange: (data: CatalogData) => void; disabled: boolean; t: typeof copy[Locale] }) {
  const { control, register, watch } = useForm<CatalogData>({ defaultValues: data });
  const categories = useFieldArray({ control, name: 'categories' });
  useEffect(() => {
    const subscription = watch((value) => {
      if (value.categories) onChange({ categories: value.categories as CatalogCategory[] });
    });
    return () => subscription.unsubscribe();
  }, [onChange, watch]);

  return <div className="catalog-editor">
    {categories.fields.map((category, categoryIndex) => <fieldset className="catalog-category" key={category.id}>
      <div className="catalog-edit-heading"><label>{t.categoryName}<input {...register(`categories.${categoryIndex}.name` as const)} disabled={disabled} required /></label><button type="button" className="text-button danger" disabled={disabled} onClick={() => categories.remove(categoryIndex)}>{t.remove}</button></div>
      <ProductFields control={control} register={register} categoryIndex={categoryIndex} disabled={disabled} t={t} />
    </fieldset>)}
    <button type="button" className="button quiet" disabled={disabled} onClick={() => categories.append({ key: newKey('category'), name: '', position: categories.fields.length, products: [] })}>{t.addCategory}</button>
  </div>;
}

function ProductFields({ control, register, categoryIndex, disabled, t }: { control: Control<CatalogData>; register: UseFormRegister<CatalogData>; categoryIndex: number; disabled: boolean; t: typeof copy[Locale] }) {
  const name = `categories.${categoryIndex}.products` as const;
  const products = useFieldArray({ control, name });
  return <>
    {products.fields.map((product, productIndex) => <fieldset className="catalog-product" key={product.id}>
      <div className="catalog-edit-heading"><label>{t.productName}<input {...register(`categories.${categoryIndex}.products.${productIndex}.name` as const)} disabled={disabled} required /></label><button type="button" className="text-button danger" disabled={disabled} onClick={() => products.remove(productIndex)}>{t.remove}</button></div>
      <label className="catalog-field">{t.description}<textarea {...register(`categories.${categoryIndex}.products.${productIndex}.description` as const)} disabled={disabled} rows={2} /></label>
      <div className="catalog-fields"><label>{t.basePrice}<input inputMode="numeric" pattern="[0-9]*" {...register(`categories.${categoryIndex}.products.${productIndex}.base_price_minor` as const, { setValueAs: (value) => String(value) })} disabled={disabled} required /></label><label className="catalog-checkbox"><input type="checkbox" {...register(`categories.${categoryIndex}.products.${productIndex}.active` as const)} disabled={disabled} />{t.active}</label></div>
      <OptionGroupFields control={control} register={register} categoryIndex={categoryIndex} productIndex={productIndex} disabled={disabled} t={t} />
    </fieldset>)}
    <button type="button" className="button small quiet" disabled={disabled} onClick={() => products.append({ key: newKey('product'), name: '', description: '', base_price_minor: '0', position: products.fields.length, active: true, option_groups: [] })}>{t.addProduct}</button>
  </>;
}

function OptionGroupFields({ control, register, categoryIndex, productIndex, disabled, t }: { control: Control<CatalogData>; register: UseFormRegister<CatalogData>; categoryIndex: number; productIndex: number; disabled: boolean; t: typeof copy[Locale] }) {
  const name = `categories.${categoryIndex}.products.${productIndex}.option_groups` as const;
  const groups = useFieldArray({ control, name });
  return <>
    {groups.fields.map((group, groupIndex) => <OptionFields key={group.id} control={control} register={register} categoryIndex={categoryIndex} productIndex={productIndex} groupIndex={groupIndex} disabled={disabled} t={t} onRemove={() => groups.remove(groupIndex)} />)}
    <button type="button" className="button small quiet" disabled={disabled} onClick={() => groups.append({ key: newKey('group'), name: '', min_select: 0, max_select: 1, position: groups.fields.length, options: [{ key: newKey('option'), name: '', price_delta_minor: '0', position: 0, active: true }] })}>{t.addGroup}</button>
  </>;
}

function OptionFields({ control, register, categoryIndex, productIndex, groupIndex, disabled, t, onRemove }: { control: Control<CatalogData>; register: UseFormRegister<CatalogData>; categoryIndex: number; productIndex: number; groupIndex: number; disabled: boolean; t: typeof copy[Locale]; onRemove: () => void }) {
  const name = `categories.${categoryIndex}.products.${productIndex}.option_groups.${groupIndex}.options` as const;
  const options = useFieldArray({ control, name });
  return <fieldset className="catalog-group">
    <div className="catalog-edit-heading"><legend>{t.optionGroup}</legend><button type="button" className="text-button danger" disabled={disabled} onClick={onRemove}>{t.remove}</button></div>
    <div className="catalog-fields"><label>{t.groupName}<input {...register(`categories.${categoryIndex}.products.${productIndex}.option_groups.${groupIndex}.name` as const)} disabled={disabled} required /></label><label>{t.min}<input type="number" min={0} max={options.fields.length} {...register(`categories.${categoryIndex}.products.${productIndex}.option_groups.${groupIndex}.min_select` as const, { valueAsNumber: true })} disabled={disabled} /></label><label>{t.max}<input type="number" min={1} max={options.fields.length} {...register(`categories.${categoryIndex}.products.${productIndex}.option_groups.${groupIndex}.max_select` as const, { valueAsNumber: true })} disabled={disabled} /></label></div>
    {options.fields.map((option, optionIndex) => <div className="catalog-option-edit" key={option.id}><label>{t.optionName}<input {...register(`categories.${categoryIndex}.products.${productIndex}.option_groups.${groupIndex}.options.${optionIndex}.name` as const)} disabled={disabled} required /></label><label>{t.extraPrice}<input inputMode="numeric" pattern="[0-9]*" {...register(`categories.${categoryIndex}.products.${productIndex}.option_groups.${groupIndex}.options.${optionIndex}.price_delta_minor` as const, { setValueAs: (value) => String(value) })} disabled={disabled} required /></label><label className="catalog-checkbox"><input type="checkbox" {...register(`categories.${categoryIndex}.products.${productIndex}.option_groups.${groupIndex}.options.${optionIndex}.active` as const)} disabled={disabled} />{t.active}</label><button type="button" className="text-button danger" disabled={disabled} onClick={() => options.remove(optionIndex)}>{t.remove}</button></div>)}
    <button type="button" className="button small quiet" disabled={disabled} onClick={() => options.append({ key: newKey('option'), name: '', price_delta_minor: '0', position: options.fields.length, active: true })}>{t.addOption}</button>
  </fieldset>;
}

function PreviewPanel({ preview, candidate, published, t }: { preview: CompanyPreview; candidate: CatalogData; published: CompanyPublished; t: typeof copy[Locale] }) {
  const names = useMemo(() => nameIndex(candidate, published), [candidate, published]);
  const changes = useMemo(() => summarizeChanges(published, candidate, t), [candidate, published, t]);
  return <section className={`catalog-preview ${preview.can_publish ? 'is-clear' : 'has-issues'}`} aria-label={t.preview}>
    <div className="catalog-change-summary" aria-label={t.changeSummaryTitle}>
      <h3>{t.changeSummaryTitle}</h3><p>{t.changeSummaryAdvisory}</p>
      {changes.length === 0 ? <p>{t.noCatalogChanges}</p> : <ul>{changes.map((change) => <li key={change.kind}>
        <strong>{t[change.kind]} ({change.items.length})</strong> {change.items.slice(0, 4).join(' · ')}{change.items.length > 4 ? ` · ${t.moreItems(change.items.length - 4)}` : ''}
      </li>)}</ul>}
      <p>{t.linkedCount}: {preview.linked_store_count}</p>
    </div>
    <h2>{t.preview}</h2><p>{preview.can_publish ? t.noIssues : t.previewBlocked}</p>
    {preview.issues.length > 0 && <ul>{preview.issues.map((issue, index) => <li key={`${issue.code}:${issue.product_key}:${issue.group_key}:${issue.store_id}:${index}`}>
      <strong>{names.product(issue.product_key)}</strong>{issue.group_key && <span> · {names.group(issue.product_key, issue.group_key)}</span>}
      {issue.store_name && <span> · {issue.store_name}</span>}<p>{issueText(issue, t)}{issue.required_count !== undefined && issue.available_count !== undefined ? ` (${issue.available_count}/${issue.required_count})` : ''}</p>
    </li>)}</ul>}
  </section>;
}

function StoreCatalogView({ menu, priceInputs, setPriceInputs, reasons, setReasons, pauseTarget, setPauseTarget, busy, onPrice, onAvailability, t }: {
  menu: StoreCatalog; priceInputs: Record<string, string>; setPriceInputs: (next: Record<string, string>) => void;
  reasons: Record<string, string>; setReasons: (next: Record<string, string>) => void; pauseTarget: string; setPauseTarget: (key: string) => void; busy: boolean;
  onPrice: (product: CatalogProduct, reset: boolean) => void;
  onAvailability: (targetKey: string, target: { kind: 'PRODUCT' | 'OPTION'; product_key: string; group_key?: string; option_key?: string }, status: Availability, version: number) => void;
  t: typeof copy[Locale];
}) {
  return <div className="catalog-store-list">{menu.categories.map((category) => <section className="catalog-store-category" key={category.key}><h2>{category.name}</h2>
    {category.products.map((product) => <article className="catalog-store-product" key={product.key}>
      <header><div><h3>{product.name}</h3><p>{product.description}</p></div><AvailabilityBadge value={product.availability ?? 'PENDING_CONFIRMATION'} t={t} /></header>
      <div className={`catalog-price-grid ${menu.mode === 'DETACHED' ? 'is-detached' : ''}`}><div><span>{menu.mode === 'LINKED' ? t.sourcePrice : t.detachedBasePrice}</span><strong>{amountJPY(product.base_price_minor)}</strong></div>{menu.mode === 'LINKED' && <div><label>{t.overridePrice}<input inputMode="numeric" pattern="[0-9]*" value={priceInputs[product.key] ?? ''} disabled={!menu.can_edit_prices || busy} onChange={(event) => setPriceInputs({ ...priceInputs, [product.key]: event.target.value })} /></label></div>}<div><span>{t.effectivePrice}</span><strong>{amountJPY(product.effective_price_minor)}</strong></div></div>
      {menu.mode === 'LINKED' && menu.can_edit_prices && <div className="catalog-price-actions"><button type="button" className="button small quiet" disabled={busy} onClick={() => onPrice(product, false)}>{t.savePrice}</button>{product.override_active && <button type="button" className="text-button" disabled={busy} onClick={() => onPrice(product, true)}>{t.resetPrice}</button>}</div>}
      <AvailabilityControl targetKey={`p:${product.key}`} label={t.availability} value={product.availability ?? 'PENDING_CONFIRMATION'} version={product.availability_version ?? 0} target={{ kind: 'PRODUCT', product_key: product.key }} canPause={menu.can_pause_availability} canConfirm={menu.can_confirm_availability} reason={reasons[`p:${product.key}`] ?? ''} setReason={(value) => setReasons({ ...reasons, [`p:${product.key}`]: value })} pauseTarget={pauseTarget} setPauseTarget={setPauseTarget} busy={busy} onChange={onAvailability} t={t} />
      {product.option_groups.map((group) => <section className="catalog-store-group" key={group.key}><h4>{group.name}</h4>{group.options.map((option) => <div className="catalog-store-option" key={option.key}><span>{option.name}{positiveAmount(option.price_delta_minor) ? ` · +${amountJPY(option.price_delta_minor)}` : ''}</span><AvailabilityBadge value={option.availability ?? 'PENDING_CONFIRMATION'} t={t} /><AvailabilityControl targetKey={`o:${product.key}:${group.key}:${option.key}`} label={t.availability} value={option.availability ?? 'PENDING_CONFIRMATION'} version={option.availability_version ?? 0} target={{ kind: 'OPTION', product_key: product.key, group_key: group.key, option_key: option.key }} canPause={menu.can_pause_availability} canConfirm={menu.can_confirm_availability} reason={reasons[`o:${product.key}:${group.key}:${option.key}`] ?? ''} setReason={(value) => setReasons({ ...reasons, [`o:${product.key}:${group.key}:${option.key}`]: value })} pauseTarget={pauseTarget} setPauseTarget={setPauseTarget} busy={busy} onChange={onAvailability} t={t} /></div>)}</section>)}
    </article>)}
  </section>)}</div>;
}

function AvailabilityControl({ targetKey, label, value, version, target, canPause, canConfirm, reason, setReason, pauseTarget, setPauseTarget, busy, onChange, t }: {
  targetKey: string; label: string; value: Availability; version: number; target: { kind: 'PRODUCT' | 'OPTION'; product_key: string; group_key?: string; option_key?: string };
  canPause: boolean; canConfirm: boolean; reason: string; setReason: (value: string) => void; pauseTarget: string; setPauseTarget: (value: string) => void; busy: boolean;
  onChange: (targetKey: string, target: { kind: 'PRODUCT' | 'OPTION'; product_key: string; group_key?: string; option_key?: string }, status: Availability, version: number) => void; t: typeof copy[Locale];
}) {
  const isPauseEditing = pauseTarget === targetKey;
  return <div className="catalog-availability-controls" aria-label={label}>
    {value === 'PENDING_CONFIRMATION' && canConfirm && <button type="button" className="button small primary" disabled={busy} onClick={() => onChange(targetKey, target, 'AVAILABLE', version)}>{t.confirmSale}</button>}
    {value === 'AVAILABLE' && canPause && !isPauseEditing && <button type="button" className="button small quiet" disabled={busy} onClick={() => setPauseTarget(targetKey)}>{t.pause}</button>}
    {value === 'PAUSED' && canConfirm && <button type="button" className="button small quiet" disabled={busy} onClick={() => onChange(targetKey, target, 'AVAILABLE', version)}>{t.reopen}</button>}
    {isPauseEditing && <form className="catalog-pause-form" onSubmit={(event) => { event.preventDefault(); onChange(targetKey, target, 'PAUSED', version); }}><label>{t.reason}<input value={reason} onChange={(event) => setReason(event.target.value)} required /></label><button type="button" className="text-button" disabled={busy} onClick={() => setPauseTarget('')}>{t.cancel}</button><button type="submit" className="button small quiet" disabled={busy || !reason.trim()}>{t.pause}</button></form>}
  </div>;
}

function AvailabilityBadge({ value, t }: { value: Availability; t: typeof copy[Locale] }) {
  const label = value === 'AVAILABLE' ? t.available : value === 'PAUSED' ? t.paused : t.pending;
  return <span className={`availability-badge availability-${value.toLowerCase()}`}>{label}</span>;
}

type ChangeKind = 'addedCategory' | 'removedCategory' | 'addedProduct' | 'removedProduct' | 'addedGroup' | 'removedGroup' | 'addedOption' | 'removedOption' | 'changedText' | 'changedPrice' | 'changedRequiredRule' | 'changedSalesStatus';
interface CatalogChange { kind: ChangeKind; items: string[] }

function summarizeChanges(published: CatalogData, candidate: CatalogData, t: typeof copy[Locale]): CatalogChange[] {
  const rows = new Map<ChangeKind, string[]>();
  const add = (kind: ChangeKind, item: string) => rows.set(kind, [...(rows.get(kind) ?? []), item]);
  const oldCategories = new Map(published.categories.map((item) => [item.key, item]));
  const newCategories = new Map(candidate.categories.map((item) => [item.key, item]));
  for (const [key, category] of newCategories) if (!oldCategories.has(key)) add('addedCategory', category.name);
  for (const [key, category] of oldCategories) if (!newCategories.has(key)) add('removedCategory', category.name);
  for (const [key, current] of newCategories) {
    const old = oldCategories.get(key);
    if (old && old.name !== current.name) add('changedText', `${old.name} → ${current.name}`);
  }

  const products = (catalog: CatalogData) => catalog.categories.flatMap((category) => category.products.map((product) => ({ product, categoryName: category.name })));
  const oldProducts = new Map(products(published).map((row) => [row.product.key, row]));
  const newProducts = new Map(products(candidate).map((row) => [row.product.key, row]));
  for (const [key, row] of newProducts) if (!oldProducts.has(key)) add('addedProduct', row.product.name);
  for (const [key, row] of oldProducts) if (!newProducts.has(key)) add('removedProduct', row.product.name);
  for (const [key, row] of newProducts) {
    const previous = oldProducts.get(key)?.product;
    if (!previous) continue;
    if (previous.name !== row.product.name) add('changedText', `${previous.name} → ${row.product.name}`);
    else if ((previous.description ?? '') !== (row.product.description ?? '')) add('changedText', `${row.product.name} (${t.description})`);
    if (String(previous.base_price_minor) !== String(row.product.base_price_minor)) add('changedPrice', `${row.product.name}: ${amountJPY(previous.base_price_minor)} → ${amountJPY(row.product.base_price_minor)}`);
    if (previous.active !== row.product.active) add('changedSalesStatus', row.product.name);
  }

  const groups = (catalog: CatalogData) => products(catalog).flatMap(({ product }) => product.option_groups.map((group) => ({ productKey: product.key, productName: product.name, group })));
  const groupIdentity = (row: ReturnType<typeof groups>[number]) => `${row.productKey}\0${row.group.key}`;
  const oldGroups = new Map(groups(published).map((row) => [groupIdentity(row), row]));
  const newGroups = new Map(groups(candidate).map((row) => [groupIdentity(row), row]));
  for (const [key, row] of newGroups) if (!oldGroups.has(key)) add('addedGroup', `${row.productName} / ${row.group.name}`);
  for (const [key, row] of oldGroups) if (!newGroups.has(key)) add('removedGroup', `${row.productName} / ${row.group.name}`);
  for (const [key, row] of newGroups) {
    const previous = oldGroups.get(key)?.group;
    if (!previous) continue;
    if (previous.name !== row.group.name) add('changedText', `${previous.name} → ${row.group.name}`);
    if (previous.min_select !== row.group.min_select || previous.max_select !== row.group.max_select) {
      add('changedRequiredRule', `${row.productName} / ${row.group.name}: ${previous.min_select}–${previous.max_select} → ${row.group.min_select}–${row.group.max_select}`);
    }
  }

  const options = (catalog: CatalogData) => groups(catalog).flatMap(({ productKey, productName, group }) => group.options.map((option) => ({ productKey, productName, groupName: group.name, groupKey: group.key, option })));
  const optionIdentity = (row: ReturnType<typeof options>[number]) => `${row.productKey}\0${row.groupKey}\0${row.option.key}`;
  const oldOptions = new Map(options(published).map((row) => [optionIdentity(row), row]));
  const newOptions = new Map(options(candidate).map((row) => [optionIdentity(row), row]));
  for (const [key, row] of newOptions) if (!oldOptions.has(key)) add('addedOption', `${row.productName} / ${row.option.name}`);
  for (const [key, row] of oldOptions) if (!newOptions.has(key)) add('removedOption', `${row.productName} / ${row.option.name}`);
  for (const [key, row] of newOptions) {
    const previous = oldOptions.get(key)?.option;
    if (!previous) continue;
    if (previous.name !== row.option.name) add('changedText', `${previous.name} → ${row.option.name}`);
    if (String(previous.price_delta_minor) !== String(row.option.price_delta_minor)) add('changedPrice', `${row.productName} / ${row.option.name}: ${amountJPY(previous.price_delta_minor)} → ${amountJPY(row.option.price_delta_minor)}`);
    if (previous.active !== row.option.active) add('changedSalesStatus', `${row.productName} / ${row.option.name}`);
  }

  return [...rows].map(([kind, items]) => ({ kind, items }));
}

function sameCatalog(a: CatalogData, b: CatalogData) { return JSON.stringify(a.categories) === JSON.stringify(b.categories); }
function canWriteDraft(draft: CompanyDraft) { try { toNumericDraft(draft); return true; } catch { return false; } }
function positiveAmount(value: string | number) { try { return BigInt(String(value)) > 0n; } catch { return false; } }
function newKey(prefix: string) { return `${prefix}-${globalThis.crypto?.randomUUID?.().replaceAll('-', '') ?? `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`}`; }
function isConflict(error: unknown) { return error instanceof ApiError && error.status === 409; }
function errorCopy(error: unknown, locale: Locale) {
  if (error instanceof ApiError && (error.status === 401 || error.status === 403)) return copy[locale].unauthorized;
  if (error instanceof ApiError && error.status === 409) return copy[locale].stale;
  return error instanceof Error && error.message === 'amount_precision_unsupported' ? copy[locale].amountUnsupported : copy[locale].network;
}
function nameIndex(...catalogs: Array<CatalogData | null>) {
  const products = new Map<string, string>(); const groups = new Map<string, string>();
  for (const catalog of catalogs) for (const category of catalog?.categories ?? []) for (const product of category.products) {
    products.set(product.key, product.name); for (const group of product.option_groups) groups.set(`${product.key}\0${group.key}`, group.name);
  }
  return { product: (key: string) => products.get(key) ?? key, group: (product: string, group: string) => groups.get(`${product}\0${group}`) ?? group };
}
function issueText(issue: PublicationIssue, t: typeof copy[Locale]) {
  if (issue.code === 'NEW_REQUIRED_GROUP') return t.issueNewGroup;
  if (issue.code === 'OPTIONAL_GROUP_BECAME_REQUIRED') return t.issueOptionalRequired;
  if (issue.code === 'REQUIRED_GROUP_MIN_INCREASED') return t.issueMinIncrease;
  if (issue.code === 'REQUIRED_OPTION_MIN_UNAVAILABLE') return t.issueChoiceShortage;
  return t.issueFallback;
}
