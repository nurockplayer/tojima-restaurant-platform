# DEMO-01 — 博多餃子酒場 シロナガスクジラ / Shironagasukujira

**Reference-only dataset — not the current owner-verified in-store menu.**
**Linear:** [P-85](https://linear.app/higashijima-manage-system/issue/P-85) · [JA/ZH evidence](https://linear.app/higashijima-manage-system/document/mod-05-jazhdemo-01-シロナガスクジラ-店家菜單採集與測試種子-v01-f11aa97cd49c)

## What is provided / 提供内容
- [Full source-labelled JSON evidence (32 items)](./shironagasukujira-menu-evidence.json), with nine source/categorization keys, provenance references, historical prices, owner-verification flags and test-only guardrails.
- **9 DINE_IN_HISTORICAL** items: third-party historical dine-in prices; owner_verified=false. orderable_in_demo=true means only in disposable test fixture, NOT real checkout.
- **8 DINE_IN_UNPRICED** items: referenced product names without verified prices; never orderable.
- **15 DELIVERY_ONLY** items: Uber Eats price references. Never populate dine-in price/quote/order/receipt from these.
- One historical rice-size example for シロナガスクジラ定食: ordinary ¥980 vs small ¥960 in a June 2026 customer report. Do not extrapolate to other dishes.

## Contract / 契約
1. Use a synthetic, isolated demo tenant/store. The JSON seed_tenant and seed_store are fixture LABELS, not real company/store IDs. Never seed a real tenant/production database.
2. Use the existing Go tenant/store/catalog/table/order APIs. Add no restaurant-specific business logic or duplicate ingredient/recipe masters.
3. Keep each source_id and reference_channel. For historical prices show a persistent **DEMO / 参考価格 / 現行店内価格未確認** label. No price means DRAFT/disabled; delivery-only means excluded from dine-in ordering.
4. Respect production_import_allowed=false and live_payment_enabled=false. Every item also has eligible_for_real_payment=false. Disable LIVE Stripe, cash settlement/real receipts, production POS and KDS hardware/printing **server-side**. Local fake/test payment and synthetic KDS acceptance only.
5. Do not copy or redistribute photos from Tabelog, social posts or delivery apps without reuse rights. Use text or neutral placeholders until the owner provides authorized assets.
6. Acceptance: three devices join the same QR/table; shared cart add/remove/quantity/option; exactly one committed order per cart generation; synthetic staff S02 and KDS; cash/close test-only. Store isolation and no real charges must be asserted automatically.
7. Before any real merchant activation: 東島さん must verify current in-store tax-inclusive names/prices, portions, hours/lunch versus dinner, option prices, alcohol items, photography rights, real merchant/payment recipient, and store QR/staff policies.
8. This data/docs-only branch is intentionally separate from ongoing P-79–P-84 engineering work. Astra can implement a minimal importer/fixture in a bounded PR only after preserving all test/payment guards.

## Sources / 出典 / 資料來源
- Official LINE: https://page.line.me/268exiib
- Historical menu photos: https://tabelog.com/tokyo/A1309/A130905/13244567/dtlmenu/photo/
- June 2026 review: https://tabelog.com/tokyo/A1309/A130905/13244567/dtlrvwlst/B530472747/
- October 2025 QR-order report: https://tabelog.com/tokyo/A1309/A130905/13244567/dtlrvwlst/B515820731/
- February 2025 dinner report: https://ameblo.jp/ameblo-bob22/entry-12888696152.html
- Delivery-only listing: https://www.ubereats.com/jp/store/%E5%8D%9A%E5%A4%9A%E9%A4%83%E5%AD%90%E9%85%92%E5%A0%B4-%E3%82%B7%E3%83%AD%E3%83%8A%E3%82%AC%E3%82%B9%E3%82%AF%E3%82%B8%E3%83%A9/nUc-CCyaTyGBkvXcdx5ddw

## 日本語
**今の店舗公式メニューとして認定されていません。** 店内歴史価格と配達価格を混同せず、実運営店に投入しないでください。テスト専用の店舗とダミー決済/KDSに限り利用し、店の現行価格・営業時間・写真権利はOwner承認後に更新します。

## 繁體中文
本資料**不是店家核准的現行菜單**，只是32項候選（9項歷史內用價、8項內用品名但無價、15項外送參考價）。只供隔離 DEMO 測試，不得直接連真實 Stripe、收銀／收據、餐廳廚房。要正式上線須請東島さん確認最新店內售價、餐點/酒水、加料、使用權及營運規範。
