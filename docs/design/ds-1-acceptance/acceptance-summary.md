# DS-1 rendered acceptance (director)

before = origin/main 78baf00, after = cebe5b3 (DS-1 + ORDER_READ DEMO notice, merged with 78baf00). Fresh isolated demo per side (operationstest -demo-shironagasukujira -catalog-fixture, disposable PostgreSQL 17), headless Chrome via CDP, computed styles. Audit = every visible text node + every non-empty input/select/textarea value vs nearest opaque ancestor background; AA 4.5 (3.0 large); disabled exempt; dedupe key includes threshold and disabled state.

| state | before fails / samples / min px / notice | after fails / samples / min px / notice | after overflow |
|---|---|---|---|
| guest-ja-375-01-menu | 17 / 48 / 8.33333 / DEMO | 0 / 43 / 12 / DEMO | 375/375 |
| guest-ja-375-02-cart | 17 / 53 / 8.33333 / DEMO | 0 / 47 / 12 / DEMO | 375/375 |
| guest-ja-375-04-preview | 14 / 31 / 9 / DEMO | 0 / 26 / 12 / DEMO | 375/375 |
| guest-ja-375-05-orders | 11 / 29 / 9 / DEMO | 0 / 26 / 12 / DEMO | 375/375 |
| guest-ja-375-06-order-read | 1 / 6 / 9 / — | 0 / 8 / 12 / DEMO | 375/375 |
| guest-zh-375-01-menu | 17 / 49 / 8.33333 / DEMO | 0 / 44 / 12 / DEMO | 375/375 |
| guest-zh-375-02-cart | 17 / 54 / 8.33333 / DEMO | 0 / 48 / 12 / DEMO | 375/375 |
| guest-zh-375-04-preview | 14 / 32 / 9 / DEMO | 0 / 27 / 12 / DEMO | 375/375 |
| guest-zh-375-05-orders | 11 / 29 / 9 / DEMO | 0 / 26 / 12 / DEMO | 375/375 |
| guest-zh-375-06-order-read | 1 / 6 / 9 / — | 0 / 8 / 12 / DEMO | 375/375 |
| guest-ja-1280-01-menu | 17 / 51 / 8.33333 / DEMO | 0 / 46 / 12 / DEMO | 1280/1280 |
| guest-ja-1280-02-cart | 17 / 56 / 8.33333 / DEMO | 0 / 50 / 12 / DEMO | 1280/1280 |
| guest-ja-1280-04-preview | 14 / 33 / 9 / DEMO | 0 / 28 / 12 / DEMO | 1280/1280 |
| guest-ja-1280-05-orders | 11 / 31 / 9 / DEMO | 0 / 28 / 12 / DEMO | 1280/1280 |
| guest-ja-1280-06-order-read | 1 / 6 / 9 / — | 0 / 8 / 12 / DEMO | 1280/1280 |
| guest-zh-1280-01-menu | 17 / 51 / 8.33333 / DEMO | 0 / 46 / 12 / DEMO | 1280/1280 |
| guest-zh-1280-02-cart | 17 / 56 / 8.33333 / DEMO | 0 / 50 / 12 / DEMO | 1280/1280 |
| guest-zh-1280-04-preview | 14 / 33 / 9 / DEMO | 0 / 28 / 12 / DEMO | 1280/1280 |
| guest-zh-1280-05-orders | 11 / 31 / 9 / DEMO | 0 / 28 / 12 / DEMO | 1280/1280 |
| guest-zh-1280-06-order-read | 1 / 6 / 9 / — | 0 / 8 / 12 / DEMO | 1280/1280 |
| guest-ja-320-menu | 17 / 49 / 8.33333 / DEMO | 0 / 44 / 12 / DEMO | 320/320 |
| staff-ja-1280-01-tables | 8 / 21 / 9 / DEMO | 0 / 19 / 12 / DEMO | 1280/1280 |
| staff-ja-1280-02-detail | 9 / 26 / 9 / DEMO | 0 / 20 / 12 / DEMO | 1280/1280 |
| staff-ja-1280-03-dialog | 10 / 34 / 9 / DEMO | 0 / 28 / 12 / DEMO | 1280/1280 |
| staff-zh-1280-01-tables | 8 / 21 / 9 / DEMO | 0 / 19 / 12 / DEMO | 1280/1280 |
| staff-zh-1280-02-detail | 9 / 27 / 9 / DEMO | 0 / 21 / 12 / DEMO | 1280/1280 |
| staff-zh-1280-03-dialog | 10 / 35 / 9 / DEMO | 0 / 29 / 12 / DEMO | 1280/1280 |
| staff-ja-375-01-tables | 8 / 20 / 9 / DEMO | 0 / 18 / 12 / DEMO | 375/375 |
| staff-ja-375-02-detail | 9 / 26 / 9 / DEMO | 0 / 20 / 12 / DEMO | 375/375 |
| staff-ja-375-03-dialog | 10 / 34 / 9 / DEMO | 0 / 28 / 12 / DEMO | 375/375 |
| staff-zh-375-01-tables | 8 / 20 / 9 / DEMO | 0 / 18 / 12 / DEMO | 375/375 |
| staff-zh-375-02-detail | 9 / 26 / 9 / DEMO | 0 / 20 / 12 / DEMO | 375/375 |
| staff-zh-375-03-dialog | 10 / 34 / 9 / DEMO | 0 / 28 / 12 / DEMO | 375/375 |
| kds-ja-1280 | 9 / 23 / 9 / DEMO | 0 / 19 / 12 / DEMO | 1280/1280 |
| catalog-ja-1280 | 12 / 79 / 9 / DEMO | 0 / 76 / 12 / DEMO | 1280/1280 |
| kds-zh-1280 | 9 / 23 / 9 / DEMO | 0 / 19 / 12 / DEMO | 1280/1280 |
| catalog-zh-1280 | 12 / 79 / 9 / DEMO | 0 / 76 / 12 / DEMO | 1280/1280 |
| kds-ja-375 | 9 / 22 / 9 / DEMO | 0 / 18 / 12 / DEMO | 375/375 |
| catalog-ja-375 | 12 / 79 / 9 / DEMO | 0 / 76 / 12 / DEMO | 375/375 |
| kds-zh-375 | 9 / 22 / 9 / DEMO | 0 / 18 / 12 / DEMO | 375/375 |
| catalog-zh-375 | 12 / 79 / 9 / DEMO | 0 / 76 / 12 / DEMO | 375/375 |

Totals: 41 states; failures before 449 → after 0; DEMO notice missing before 4 → after 0; overflow after 0.

## Keyboard focus (before → after)

- guest-ja-375-focus-stepper: BUTTON '＋' solid 3px rgb(210, 163, 107) off 2px clip stepper → BUTTON '＋' solid 3px rgb(11, 95, 204) off -3px clip None
- guest-ja-375-focus-stepper-minus: BUTTON '−' solid 3px rgb(210, 163, 107) off 2px clip stepper → BUTTON '−' solid 3px rgb(11, 95, 204) off -3px clip None
- guest-ja-375-focus-link: A 'b2f630265dda' auto 1px rgb(0, 95, 204) off 1px clip None → A 'd3f5b3e2259a' solid 3px rgb(11, 95, 204) off 2px clip None
- guest-zh-375-focus-stepper: BUTTON '＋' solid 3px rgb(210, 163, 107) off 2px clip stepper → BUTTON '＋' solid 3px rgb(11, 95, 204) off -3px clip None
- guest-zh-375-focus-stepper-minus: BUTTON '−' solid 3px rgb(210, 163, 107) off 2px clip stepper → BUTTON '−' solid 3px rgb(11, 95, 204) off -3px clip None
- guest-zh-375-focus-link: A 'dd399e42951c' auto 1px rgb(0, 95, 204) off 1px clip None → A '6e81abc9032b' solid 3px rgb(11, 95, 204) off 2px clip None
- guest-ja-1280-focus-stepper: BUTTON '＋' solid 3px rgb(210, 163, 107) off 2px clip stepper → BUTTON '＋' solid 3px rgb(11, 95, 204) off -3px clip None
- guest-ja-1280-focus-stepper-minus: BUTTON '−' solid 3px rgb(210, 163, 107) off 2px clip stepper → BUTTON '−' solid 3px rgb(11, 95, 204) off -3px clip None
- guest-ja-1280-focus-link: A '072712d409a8' auto 1px rgb(0, 95, 204) off 1px clip None → A '8a4827b53ae4' solid 3px rgb(11, 95, 204) off 2px clip None
- guest-zh-1280-focus-stepper: BUTTON '＋' solid 3px rgb(210, 163, 107) off 2px clip stepper → BUTTON '＋' solid 3px rgb(11, 95, 204) off -3px clip None
- guest-zh-1280-focus-stepper-minus: BUTTON '−' solid 3px rgb(210, 163, 107) off 2px clip stepper → BUTTON '−' solid 3px rgb(11, 95, 204) off -3px clip None
- guest-zh-1280-focus-link: A 'c4745c8bde96' auto 1px rgb(0, 95, 204) off 1px clip None → A 'c809affc76c0' solid 3px rgb(11, 95, 204) off 2px clip None
- staff-ja-1280-dialog-focus: Tab sequence after = INPUT '' solid 3px rgb(11, 95, 204) off -3px clip None → BUTTON 'キャンセル' solid 3px rgb(11, 95, 204) off -3px clip None → BODY → INPUT '11600' solid 3px rgb(11, 95, 204) off -3px clip None → INPUT '' solid 3px rgb(11, 95, 204) off -3px clip None → BUTTON 'キャンセル' solid 3px rgb(11, 95, 204) off -3px clip None
- staff-zh-1280-dialog-focus: Tab sequence after = INPUT '' solid 3px rgb(11, 95, 204) off -3px clip None → BUTTON '取消' solid 3px rgb(11, 95, 204) off -3px clip None → BODY → INPUT '11600' solid 3px rgb(11, 95, 204) off -3px clip None → INPUT '' solid 3px rgb(11, 95, 204) off -3px clip None → BUTTON '取消' solid 3px rgb(11, 95, 204) off -3px clip None
- staff-ja-375-dialog-focus: Tab sequence after = INPUT '' solid 3px rgb(11, 95, 204) off -3px clip None → BUTTON 'キャンセル' solid 3px rgb(11, 95, 204) off -3px clip None → BODY → INPUT '11600' solid 3px rgb(11, 95, 204) off -3px clip None → INPUT '' solid 3px rgb(11, 95, 204) off -3px clip None → BUTTON 'キャンセル' solid 3px rgb(11, 95, 204) off -3px clip None
- staff-zh-375-dialog-focus: Tab sequence after = INPUT '' solid 3px rgb(11, 95, 204) off -3px clip None → BUTTON '取消' solid 3px rgb(11, 95, 204) off -3px clip None → BODY → INPUT '11600' solid 3px rgb(11, 95, 204) off -3px clip None → INPUT '' solid 3px rgb(11, 95, 204) off -3px clip None → BUTTON '取消' solid 3px rgb(11, 95, 204) off -3px clip None
- catalog-focus-textarea: TEXTAREA 'DEMO / 歴史資料の' auto 1px rgb(0, 95, 204) off 0px clip None → TEXTAREA 'DEMO / 歴史資料の' solid 3px rgb(11, 95, 204) off 2px clip None

Disclosures: native modal <dialog> lets Tab leave the document after its last control (identical before/after; StaffDialog intentionally unchanged). Pre-existing zh 「待店員確認」 chip wraps at 375px before and after.