# P-85 isolated restaurant demo

Authority: [P-85](https://linear.app/higashijima-manage-system/issue/P-85), its
JA/ZH DEMO-01 source document, and [source PR #13](https://github.com/nurockplayer/tojima-restaurant-platform/pull/13)
at `d6a4d88d9df09cde986b8fc2469aab53e45cbe69`.

博多餃子酒場 シロナガスクジラ is the first Mobile Order demo scenario. This is
an executable, disposable test environment, not the restaurant's current
official menu or an operational restaurant deployment.

## Source review

The original [32-item JSON](demo/shironagasukujira-menu-evidence.json) and
[source contract](demo/README.md) are preserved. Nine historical dine-in items
are eligible only for demo ordering. Eight unpriced items remain reference
data with null prices; fifteen delivery items remain delivery evidence. Neither
group is inserted into the orderable dine-in catalog. No zero price, alcohol
authorization, opening schedule, photo permission, or current official price
is invented.

The rice-size reference applies only to シロナガスクジラ定食. The existing catalog
accepts nonnegative option deltas, so the test seed uses a ¥960 base with a
required choice: small rice adds ¥0 and ordinary rice adds ¥20. Final prices
remain ¥960 and ¥980. The source JSON keeps its original ¥980 / −¥20 evidence;
this normalization changes no production pricing rule.

## Run locally

Use Go and Node versions pinned by this repository, pnpm, and an explicitly
disposable PostgreSQL 17 database listening on loopback. Do not use production
credentials or a database tunnel to an operational environment.

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/tojima_test?sslmode=disable' \
  go run ./cmd/operationstest -demo-shironagasukujira \
  -credentials /tmp/tojima-demo-credentials.json
```

The database port above is an example; use the actual disposable database's
port. The command binds only `127.0.0.1:8082`, creates a new random schema, and
writes an owner-only credential file outside the served web directory. It
refuses to replace an existing file. The file contains the guest, staff,
kitchen, and catalog URLs plus scoped synthetic test credentials. Open the
guest URL in three independent tabs/windows and join the same 5号 table; do
not put staff credentials into URLs or source control.

Enter the file's tenant/store IDs and the appropriate test token in the staff
or catalog login form. Use `cashier` for reception, test cash and service;
`kitchen` for KDS; `manager` or `catalog_admin` for the permitted catalog
availability actions. These are isolated fixture identities, not real staff
accounts. The catalog page is provided to demonstrate availability changes.

On orderly shutdown (Ctrl-C), the command drops only its own schema and removes
only its own credential file. A restart produces a fresh demo; persistence,
public hosting and mobile devices on the restaurant network are outside this
local acceptance environment.

## Walkthrough and verification

1. Three guests join the same table URL. Add a rice-size meal, dumplings and a
   small plate; edit a quantity and rice choice from another guest; remove an
   item. Refresh visible clients and confirm the shared contents converge.
2. In the catalog console, pause an item with a reason. The guest cannot add
   or commit an unavailable selection. Restore it using an authorized manager.
3. Preview the inclusive JPY amount and commit through the test cash flow.
   Competing submissions for one cart generation must resolve to one order.
   Prices, options and tax are snapshots in the order.
4. Receive the order in the test staff console. The synthetic KDS shows one
   card for that order. Advance preparation and ready states, then mark the
   whole order served using the serving staff identity.
5. Preview and settle synthetic cash, inspect the test-only receipt, and close
   the table. Closing with unpaid or unserved orders must remain blocked.

Automated checks cover source classification, rice totals, schema/tenant
isolation, runtime guards and UI demo labeling. Run `make ci` with the explicit
test database and `make web-check`. Hosted CI uses its own PostgreSQL service
and `pnpm install --frozen-lockfile`. Browser receipts must distinguish native
Ego-lite interaction from automated service tests and from physical-device QR
scanning, which this local environment does not prove.

## Boundaries

DEMO / 参考価格 / 現行店内価格未確認 stays visible across guest, staff, kitchen
and catalog screens. Cash transactions and receipts are synthetic records in
the disposable schema. They do not collect money or produce a real restaurant
receipt. No Stripe adapter, merchant account, network payment endpoint, printer,
POS or physical KDS is configured. The demo flag rejects combination with the
payment fixture and direct card-order attempts. The production `cmd/server`
remains health-only.

Before operational use, the owner must confirm current inclusive prices,
portions/options, availability and lunch/dinner hours, alcohol/store policies,
image rights, actual tenant/store/merchant mapping, real IdP and payment
configuration, deployment and pilot acceptance. P-85 does not approve these
dependencies or implement MOD-01 recipes, P-81 emergency holds, refunds or new
authentication.

日本語：独立したテスト店舗・5号卓で共有カート、注文確定、スタッフ受付、
仮想KDS、テスト現金精算と閉卓を確認する。32項の出典を保存し、歴史参考9項のみ
注文可能。実店舗への注文・収金・印刷は行わない。

繁體中文：獨立測試店與 5 號桌沿用共編購物車、正式寫入測試訂單、店員接單、
虛擬 KDS、測試現金結帳及關桌。保留 32 項來源，僅 9 項歷史內用參考品可點選；
不代表現行店家菜單核准，也不向實際餐廳送單、收款或列印。
