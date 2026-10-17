# ADR-0005 — TypeScript guest web / 顧客Web・顧客介面

Date: 2026-10-18 JST. Status: **Accepted by Owner — React + TypeScript + Vite**.

The Owner first selected a TypeScript frontend, then explicitly selected React + Vite in this chat. The decision is recorded in [P-78](https://linear.app/higashijima-manage-system/issue/P-78#comment-133925ce). This resolves the Web/UI framework question in ADR-0003 for the MOD-05 guest MVP; it does not choose an IdP or production host.

Use `web/` in this repository for a responsive React/TypeScript client, built with Vite. Go remains the single source of business rules and transactions. Build static assets and call the same Go REST API; no separate BFF, SSR service or client-authoritative price/tax calculation. Use pnpm exclusively, a pinned `packageManager`, committed lockfile and `pnpm install --frozen-lockfile` in CI; do not add lifecycle enforcement scripts.

The shared cart displays pending edits separately from immutable committed orders. REST writes carry generation/version and idempotency; client state adopts authoritative server responses and does not let late responses overwrite a newer version. A conflict invalidates the preview and requires explicit reconfirmation. Synchronize only visible cart pages every 2–3 seconds and stop the polling timer on hide/unmount. This is a bounded MVP synchronization proposal, not a subsecond SLA; existing-order status polling remains 15–30 seconds. No CRDT, guest accounts, payment splitting or dedicated realtime service is added.

P-78 chooses STRIPE/CASH for the winning order; it does not fabricate payment success or create real Stripe Checkout. P-79/P-80 own settlement, receipt boundaries and payment integration. Keep guest TABLE_JOIN, individual ORDER_READ and original submitter CHECKOUT_RESUME capabilities separate and exclude private payment data from shared responses. Browser tests use isolated test data, not fabricated production credentials.

Official sources inspected on 2026-10-18: [React build-tool guidance](https://react.dev/learn/build-a-react-app-from-scratch), [Vite guide](https://vite.dev/guide/). Vue + Vite was a viable alternative; the Owner selected React. Exact package versions are pinned during implementation and verified against official package metadata.

日本語：Owner承認によりReact＋TypeScript＋Viteを同じrepoで採用し、pnpmで管理する。Goの価格・税・table session・cart/orderの権威は維持。UIの選択はIdP/本番hosting/Stripeアカウント承認を含まない。

繁中：本決策採用React＋TypeScript＋Vite及pnpm，解除顧客Web框架待決。伺服器負責正式價格、稅額、版本衝突和原子送單；前端只呈現及發送操作，不擴張MVP或改動既有Flutter/Firebase成本App。
