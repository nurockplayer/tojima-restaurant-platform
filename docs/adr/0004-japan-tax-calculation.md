# ADR-0004 — Japan tax calculation / 日本消費税・日本消費稅

Date: 2026-10-18 JST. Status: **Accepted implementation baseline / 実装基準承認・實作基準已批准**.

Authority: latest [P-77](https://linear.app/higashijima-manage-system/issue/P-77), [P-47](https://linear.app/higashijima-manage-system/issue/P-47), [tax specification v1.0](https://linear.app/higashijima-manage-system/document/mod-05-jazh日本消費稅既定預設版本化稅率與計算契約-v10-162792a97973) and the Owner's explicit approval on 2026-10-18. This supersedes earlier statements that an absent tax ADR blocks P-77 or that only a placeholder calculator is permitted.

## Decision / 決定・決策

Japan/JPY catalog product prices, option surcharges and store overrides are **tax-inclusive integer selling prices**. They are set by the merchant; a rate change does not automatically rewrite a selling price. The initial table-QR MVP is dine-in and uses the standard class. Eligible reduced-rate takeaway food is represented in rule data and tests; no takeaway order flow or automatic classification UI is added.

Current initial rules are standard 10% and eligible reduced food 8%, effective from 2019-10-01. The national and local consumption taxes are combined in these rates. Alcohol and restaurant dining are excluded from the reduced-food category. These rules are central legal data with identifiers, rational rates, effective date bounds, legal status/source and version; no store may submit an arbitrary rate. Future amendments require new approved schedule data. Proposed or not-yet-effective entries do not activate by merely existing. Unknown, exempt or out-of-scope classes are rejected rather than silently treated as 10%.

Official sources checked through Ego-lite on 2026-10-18: [NTA 6303, rates](https://www.nta.go.jp/taxes/shiraberu/taxanswer/shohi/6303.htm), [NTA 6102, reduced-rate scope](https://www.nta.go.jp/taxes/shiraberu/taxanswer/shohi/6102.htm), [NTA 6371, invoice rounding](https://www.nta.go.jp/taxes/shiraberu/taxanswer/shohi/6371.htm).

## Calculation / 計算

For one calculation boundary, group all tax-inclusive gross JPY amounts by their normalized rational tax rate. For rate `n/d`, compute `floor(groupGross * n / (d + n))` once for that group, using exact integer/rational arithmetic. Net equals gross minus tax. Do not round each product line and sum those rounded taxes. The NTA permits a choice of rounding direction; **floor is the Owner-approved MVP choice**, not a claim that every Japanese merchant must use floor.

Examples: gross JPY 1100 at 10% includes JPY 100 tax; gross JPY 1080 at 8% includes JPY 80 tax. Two JPY 6 lines at 10% total JPY 12 and include JPY 1 tax; rounding each line first would incorrectly produce zero. Option surcharges and quantities are part of the gross input. Invalid quantities, negative amounts, overflow, mixed tenant/store scopes, unsupported classes and ambiguous effective rules are rejected.

## Snapshots and invoice boundary / 快照・收據範圍

The calculation returns a persistable snapshot containing its date, scope, rule identities/versions, tax classes, rational rates, price mode, rounding rule, copied price inputs and grouped gross/net/tax totals. The snapshot can be restored without consulting today's rate schedule. Modifying later schedules or source slices cannot change the saved calculation.

P-78 order creation must write the accepted prices/options/publication/tax snapshot atomically with the order, and must never recompute that historical record using a later rule. P-77 provides the real engine and snapshot contract; it does not invent an order table early. A cart quote and a kitchen card are not issued invoices.

P-79/P-80 own the actual invoice boundary: Stripe per-order receipts and CASH settlements that may combine several committed orders must consistently group the intended lines and round once per rate for that invoice. They must test totals, receipt tax, Stripe/CASH amount agreement and refunds before money movement. Do not sum independently rounded order quote taxes and label that a combined invoice tax. Store issuer registration is a separate receipt setting; no registration status or identifier is fabricated.

日本語：税務 ADR 不在による P-77 HOLD は解除済み。法定初期値を日付付き中央データとして扱い、実際の Go 計算で検証する。注文の履歴税額は固定し、後日税率変更で再計算しない。外食 10% と軽減対象食品 8% を区別し、外帯 UI は今回作らない。

繁中：P-77 不再等待店別稅率核准。實作真正公式、法定版本化規則及可保存快照；正式收據／金流的彙總範圍與 E2E 留給既定 P-79／P-80。此決策沒有擴大為託管、IdP、實店資格或投產批准。
