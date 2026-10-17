# ADR-0006 — Frontend stack for MVP / フロントエンド技術選定・前端技術決策

Date: 2026-10-18 JST. Status: **Accepted by Tech Lead / Tech Lead裁定・技術負責人裁定**. Cross-reviewed by Astra (`gpt-6-astra`, read-only, verdict AGREE-WITH-CHANGES; all requested changes adopted below).

Fixed inputs, not reopened without concrete engineering evidence: TypeScript `strict` + React + Vite, pnpm with a frozen lockfile, Go REST + PostgreSQL as the single authority for prices, tax, permissions and transactions ([ADR-0005](0005-typescript-guest-web.md)). This ADR supersedes the "Go API + Next.js" sketch in the 06 architecture proposal v0.1 for web frontends; there is no SSR or BFF.

## Principles / 原則

- **Adopt on first real consumer.** A library is installed in the PR that first needs it, never ahead of time. Accepted P-78/P-79 screens are not rewritten to match this ADR; they move only when a later work package changes them.
- One `web/` Vite app, organised by surface and domain. Shared code moves to `src/shared/` when a second caller exists. No speculative primitives or generic form/table frameworks.
- The existing Flutter/Firebase MOD-01 app is not modified. Whether it is retained is the separate MIG-01 decision; new UIs for MOD-02–07 use this stack.

## Decisions / 決定・決策

| Area | Decision | Rejected (reason) | Adoption trigger |
|---|---|---|---|
| UI components | Native HTML elements plus existing tokens. When a control is genuinely complex (combo box, date/range picker, modal needing focus containment, keyboard grid), use **React Aria Components**: headless, WAI-ARIA, ja-JP/zh-TW locale and date support, styled by our own CSS | MUI / Ant Design / Mantine (own design system and styling engine, larger guest payload); shadcn/ui (needs Tailwind, a second styling system); Radix (no date/i18n primitives) | First complex control in a new screen; compare effort and bundle cost at that PR. Finance grids and shift scheduling get their own requirements before a grid library is considered |
| CSS | Plain CSS with custom-property tokens; **CSS Modules** (built into Vite) for new components; readable, unminified source | Tailwind, CSS-in-JS (runtime cost on low-end phones) | Now (convention) |
| Routing | Path → lazily loaded surface (`src/surface.tsx`), with loading state and failed-chunk reload. **TanStack Router** (code-based routes, no codegen plugin) once a surface needs nested navigation, history or validated URL state | React Router (weaker typed search params) | A surface with real multi-screen navigation; P-81 by itself is not the trigger |
| Server state | **TanStack Query** for new read/poll screens, with caches keyed by tenant/store/principal or guest grant and cleared on identity change, explicit `retry`/`refetchOnWindowFocus`/polling policy, and `AbortSignal` passed to fetch. Mutations stay explicit: caller-owned idempotency key, frozen replay payload, no automatic write retry. The P-78 cart CAS reducer and P-79 console keep their own state | Redux Toolkit / RTK Query, SWR | First new read/poll screen (e.g. P-83 guest status, admin lists) |
| Client state | `useState` / `useReducer`; narrowly scoped Context, never a shared business-data cache | Zustand, Redux, Jotai | — |
| Forms | Ordinary `onSubmit` handlers and native constraints for immediate required/range/format feedback; Go remains the validation authority and 4xx errors are mapped to fields. React form Actions optional. **React Hook Form** only for demonstrated dynamic or array forms | Formik; Zod schemas duplicating Go rules | First dynamic/array form (e.g. option-group editor) |
| API | One transport in `src/shared/http.ts` (base URL, `ApiError`, network-error classification) and typed per-domain clients. Credentials passed explicitly per call; money stays decimal strings formatted with BigInt. **OpenAPI** contract and generated TS types when an independent consumer appears (06 POS, Flutter) or DTO drift recurs; no precommitment to regenerating the Go server | tRPC, GraphQL, BFF | Now: shared transport. Later: OpenAPI |
| i18n | Per-surface ja / zh-Hant dictionaries whose key sets must be identical (typed so a missing key fails `tsc`); shared `Intl` formatters; `lang` attribute follows the selected locale; business timezone Asia/Tokyo; calendar dates (business day, shift date) kept as `YYYY-MM-DD` strings, distinct from instants | i18next, react-intl | Now for new code; existing dictionaries align when touched |
| Testing | Vitest + jsdom (existing). **Testing Library + user-event** for new component tests. **Playwright** (Chromium + WebKit iPhone/iPad) for P-84 E2E against `guesttest`/`operationstest`; emulation supplements, never replaces, physical-device acceptance. Cover focus/keyboard, touch layout, stale responses, identity change, uncertain writes and reconnect | Cypress, Jest, MSW (fetch stubs suffice) | Testing Library: next new UI test. Playwright: P-84 |
| Lint | **oxlint** pinned, `react-hooks/rules-of-hooks` and `react-hooks/exhaustive-deps` at error, run first in `pnpm check` (already in CI). No inline suppressions without a reviewed reason | ESLint + typescript-eslint: 8.71.2 peers `typescript <6.1`, incompatible with the pinned TypeScript 7.0.2 (re-evaluate if that changes); Prettier (churn) | Now |
| Offline / PWA | No service worker or offline mutations in MVP. Every screen must show connection loss and stale data, and recover uncertain writes by idempotent replay | Workbox / offline queue | Revisit for 06 POS and KDS with field evidence |

Supported browsers for acceptance: current and previous major iOS/iPadOS Safari, Android Chrome, desktop Chrome/Edge/Safari. Exact versions are fixed in the P-84 test scope.

## Implemented in this change / 本変更で実装

1. `src/shared/http.ts`: the only request implementation. Fixes a defect where `operations.ts` ignored `VITE_API_BASE_URL`, so staff and guest order-status calls went to the page origin while cart calls used the configured API. `ApiError` keeps a single identity, re-exported from `api/client.ts`.
2. `src/surface.tsx`: guest and staff surfaces are separate lazy chunks (guest initial JS 281 KB → 255 KB; the staff console is no longer downloaded by guests). Loading and failed-chunk reload states are added; `/staff`, `/staff/` and guest query strings behave as before.
3. oxlint gate in `pnpm check`. Its only finding in existing code was a dead counter in `App.tsx`, which is removed instead of being suppressed.

## Known gaps before pilot / 試験店舗前の既知課題

Not framework adoption; tracked as acceptance work: KDS polling failures are silent (`StaffApp.tsx`), staff modals lack focus containment/restoration, several touch targets are below 44 px, and the staff surface does not update `document.documentElement.lang`.

## 日本語

TypeScript strict・React・Vite・pnpm・Go単一権威は再議しない。ライブラリは「最初の実需要が出たPR」でのみ導入し、受入済みP-78/P-79画面は統一目的で書き換えない。UIは標準HTML＋既存トークン、複雑な部品のみReact Aria Components。CSSは素のCSS＋CSS Modules。ルーティングは画面単位の遅延読込、複数画面ナビゲーションが必要になった時点でTanStack Router。サーバー状態は新規の読取/ポーリング画面からTanStack Query（テナント・店舗・本人/顧客grant単位のキャッシュ、明示的な再試行・再取得方針、AbortSignal）。書込はidempotency keyと固定payloadの明示的再送のみ。フォームは通常のonSubmit、動的フォームのみReact Hook Form、検証権威はGo。API通信は`src/shared/http.ts`に一本化し、OpenAPIは独立した第2クライアント（POS／Flutter）または型ずれ再発時に導入。i18nは日本語／繁體中文のキー一致を型で保証、業務タイムゾーンはAsia/Tokyo。テストはVitest＋Testing Library、E2EはPlaywright（WebKit含む、実機受入の代替ではない）。LintはoxlintでReact Hooks規則のみ（typescript-eslintはTypeScript 7非対応）。PWA／オフライン書込はMVP外、ただし通信断・古いデータ表示・不確定書込の復旧は必須。本変更で`VITE_API_BASE_URL`無視の不具合を修正し、顧客端末がスタッフ画面のコードを読み込まないよう分割した。

## 繁體中文

TypeScript strict、React、Vite、pnpm 與 Go 單一權威不再重議。函式庫只在「第一個真實需求」的 PR 引入；已驗收的 P-78/P-79 畫面不為統一技術而重寫。UI 以原生 HTML＋既有設計 token 為主，只有複雜元件採 React Aria Components。CSS 用原生 CSS＋CSS Modules。路由先以畫面延遲載入，真正需要多畫面導航時才採 TanStack Router。伺服器狀態自新的讀取/輪詢畫面起採 TanStack Query（快取依租戶、分店、使用者或顧客 grant 區隔，明確重試與重新取得策略，傳遞 AbortSignal）；寫入只允許以 idempotency key 與凍結 payload 明確重送。表單用一般 onSubmit，動態表單才用 React Hook Form，驗證權威在 Go。API 傳輸統一於 `src/shared/http.ts`，OpenAPI 待第二個獨立客戶端（POS／Flutter）或型別不一致反覆發生時導入。i18n 以型別保證日文／繁中鍵值一致，營業時區為 Asia/Tokyo。測試用 Vitest＋Testing Library，E2E 用 Playwright（含 WebKit，不取代實機驗收）。Lint 採 oxlint，只啟用 React Hooks 規則（typescript-eslint 不支援 TypeScript 7）。PWA／離線寫入不在 MVP，但斷線、資料過期提示與不確定寫入復原為必要。本變更修正 `VITE_API_BASE_URL` 被忽略的缺陷，並拆分畫面使顧客手機不下載店員畫面程式碼。
