// DS-1 reviewed source inventory. Changes require explicit review; never regenerate in tests.
// Inheritance/compositing exceptions are source-reviewed, pending Opus rendered acceptance.
export const hexInventory: string[] = [
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-access-error) | color | #98483c",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-change-summary) | background | #fff",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-edit-heading input), .catalogScope :global(.catalog-fields input), .catalogScope :global(.catalog-option-edit input), .catalogScope :global(.catalog-field textarea), .catalogScope :global(.catalog-price-grid input), .catalogScope :global(.catalog-pause-form input) | background | #fff",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-edit-heading label), .catalogScope :global(.catalog-fields label), .catalogScope :global(.catalog-option-edit label), .catalogScope :global(.catalog-field) | color | #53665b",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-group) | background | #f4f7f2",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-panel) | background | #fff",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-preview) | background | #eff5ed",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-preview) | color | #416d50",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-preview.has-issues) | background | #fbefeb",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-preview.has-issues) | color | #98483c",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-price-grid) | border-bottom | #edf0eb",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-price-grid) | border-top | #edf0eb",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-product) | background | #fbfcfa",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-published-snapshot > section > ul > li) | border-top | #e9eee8",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-published-snapshot) | background | #f8faf7",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-store-group h4) | color | #53665b",
  "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-store-option) | border-top | #edf0eb",
  "../ui/CatalogApp.module.css |  | .holdActive | color | #98483c",
  "../ui/CatalogApp.module.css |  | .holdClear | color | #416d50",
  "../ui/CatalogApp.module.css |  | .holdLookup input | background | #fff",
  "../ui/CatalogApp.module.css |  | .holdLookup | background | #fff",
  "../ui/CatalogApp.module.css |  | .holdPanel h3 | color | #59442b",
  "../ui/CatalogApp.module.css |  | .holdPanel textarea | background | #fff",
  "../ui/CatalogApp.module.css |  | .holdPanel | background | #fffaf1",
  "../ui/CatalogApp.module.css |  | .holdPanel | border | #d7b88b",
  "../ui/CatalogApp.module.css |  | .storeHoldIndicator | color | #98483c",
  "../ui/styles.css |  | .adjustment-preview,.refund-list | background | #f2f5f0",
  "../ui/styles.css |  | .brand-mark | color | #fff",
  "../ui/styles.css |  | .button.primary | color | #fff",
  "../ui/styles.css |  | .button.quiet | color | #617269",
  "../ui/styles.css |  | .button.retry | background | #f6eadc",
  "../ui/styles.css |  | .button.retry | color | #7a4e2d",
  "../ui/styles.css |  | .cash-order-list,.blocker-list | color | #56675d",
  "../ui/styles.css |  | .cash-settings-card .field select | background | #fff",
  "../ui/styles.css |  | .empty-icon | background | #edf2ec",
  "../ui/styles.css |  | .empty-state | border | #dfe5de",
  "../ui/styles.css |  | .field input | background | #fff",
  "../ui/styles.css |  | .field | color | #53665b",
  "../ui/styles.css |  | .kds-column | background | #eef1eb",
  "../ui/styles.css |  | .kds-column>h3 span | background | #fff",
  "../ui/styles.css |  | .order-line,.order-total | color | #59685f",
  "../ui/styles.css |  | .receipt-card | background | #f2f5f0",
  "../ui/styles.css |  | .staff-order-lines li | border-bottom | #edf0eb",
  "../ui/styles.css |  | .status-chip | background | #edf1ec",
  "../ui/styles.css |  | .status-chip | color | #53665b",
  "../ui/styles.css |  | .tab span | background | #e9ede7",
  "../ui/styles.css |  | .tab.active span | background | #e0ebe3",
  "../ui/styles.css |  | .table-chip | background | #f0f4ef",
  "../ui/styles.css |  | .topbar | border-bottom | #dfe4db",
  "../ui/styles.css |  | .workspace-footer | border-top | #dfe4db",
  "../ui/styles.css |  | ::selection | background | #c6dfcf"
];

export const fontExceptions: { key: string; reason: string }[] = [];

export const unsupportedInventory: { key: string; reason: string }[] = [
  {
    "key": "../ui/guest/GuestSheet.module.css |  | .overlay | background:color-mix(in srgb, var(--color-text) 48%, transparent);color:var(--color-text) | Error: unsupported colour color-mix(in srgb, var(--color-text) 48%, transparent) -> color-mix(in srgb, var(--color-text) 48%, transparent)",
    "reason": "Translucent modal scrim has no text of its own. Dialog sets an opaque surface and foreground; scrim compositing still needs rendered acceptance."
  },
  {
    "key": "../ui/CatalogApp.module.css |  | .holdLookup | background:#fff | background with inherited foreground",
    "reason": "White lookup panel inherits body ink; descendants explicitly use ink or muted. Cascade needs rendered verification."
  },
  {
    "key": "../ui/CatalogApp.module.css |  | .holdPanel | background:#fffaf1 | background with inherited foreground",
    "reason": "Warm hold panel inherits body ink; heading and status descendants use explicit dark ink. Cascade needs rendered verification."
  },
  {
    "key": "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-panel) | background:#fff | background with inherited foreground",
    "reason": "White catalog container inherits body ink with explicit muted labels; descendant backgrounds require rendered verification."
  },
  {
    "key": "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-product) | background:#fbfcfa | background with inherited foreground",
    "reason": "Near-white product container inherits body ink; explicit child field colours remain dark. Cascade needs rendered verification."
  },
  {
    "key": "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-group) | background:#f4f7f2 | background with inherited foreground",
    "reason": "Light green group container inherits body ink; legends use action ink. Cascade needs rendered verification."
  },
  {
    "key": "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-change-summary) | background:#fff | background with inherited foreground",
    "reason": "White change summary inherits body ink; its heading explicitly uses ink. Cascade needs rendered verification."
  },
  {
    "key": "../ui/CatalogApp.module.css |  | .catalogScope :global(.catalog-store-product) | background:var(--cream) | background with inherited foreground",
    "reason": "Cream store product card inherits body ink; labels use explicit muted/action ink. Cascade needs rendered verification."
  },
  {
    "key": "../ui/OrderRead.module.css |  | .panel | background:var(--color-surface) | background with inherited foreground",
    "reason": "Order-read white panel inherits root body ink; no local foreground declaration. Rendered audit must verify descendants."
  },
  {
    "key": "../ui/styles.css |  | button:disabled | opacity:.5 | opacity requires compositing (.5)",
    "reason": "Existing inactive controls deliberately use 0.5 opacity; inactive controls are exempt from WCAG contrast. No active-state exemption."
  },
  {
    "key": "../ui/styles.css |  | .tab | background:none;color:var(--color-text-muted) | Error: unsupported colour none -> none",
    "reason": "Transparent tab text inherits the page surface; muted ink is checked on both light defaults through the semantic pair checks."
  },
  {
    "key": "../ui/styles.css |  | .tab span | background:#e9ede7 | background with inherited foreground",
    "reason": "Count pill inherits muted tab ink or active green ink; both are dark, but selector inheritance needs rendered verification."
  },
  {
    "key": "../ui/styles.css |  | .tab.active:after | background:var(--green) | background with inherited foreground",
    "reason": "Empty active-tab underline is a decorative pseudo-element with no text."
  },
  {
    "key": "../ui/styles.css |  | .button.primary:not(:disabled):hover | background:var(--green-2) | background with inherited foreground",
    "reason": "Hover inherits white primary-button text; on-action/action-hover is explicitly checked by the semantic usage pairs."
  },
  {
    "key": "../ui/styles.css |  | .button.quiet | background:transparent;color:#617269 | Error: unsupported colour transparent -> transparent",
    "reason": "Transparent quiet button inherits its cream card surface; existing dark quiet ink requires rendered verification of placement."
  },
  {
    "key": "../ui/styles.css |  | .text-button | background:none;color:var(--color-text-muted) | Error: unsupported colour none -> none",
    "reason": "Transparent text button inherits page/card surface; muted ink is checked on both light defaults by semantic usage pairs."
  },
  {
    "key": "../ui/styles.css |  | .order-card | background:var(--cream) | background with inherited foreground",
    "reason": "Cream order card inherits body ink; explicit method/status descendants need rendered verification."
  },
  {
    "key": "../ui/styles.css |  | .entry-card | background:var(--cream) | background with inherited foreground",
    "reason": "Cream entry card inherits body ink; field and hint descendants need rendered cascade verification."
  },
  {
    "key": "../ui/styles.css |  | .field input | background:#fff | background with inherited foreground",
    "reason": "White native input inherits field font/colour through browser control styling; rendered audit must verify computed input text."
  },
  {
    "key": "../ui/styles.css |  | .staff-table-card,.cash-settings-card,.staff-order-card,.confirm-dialog | background:var(--cream) | background with inherited foreground",
    "reason": "Cream staff containers inherit body ink; shared rule has varied descendants requiring the staff/dialog rendered audit."
  },
  {
    "key": "../ui/styles.css |  | .cash-settings-card .field select | background:#fff | background with inherited foreground",
    "reason": "White native select relies on browser/inherited foreground; rendered audit must verify native computed text."
  },
  {
    "key": "../ui/styles.css |  | .kds-column | background:#eef1eb | background with inherited foreground",
    "reason": "Light KDS column inherits body ink; no dark scheme is introduced. Nested staff cards need rendered verification."
  },
  {
    "key": "../ui/styles.css |  | .dialog-backdrop | background:rgba(25,39,32,.45) | Error: unsupported colour rgba(25,39,32,.45) -> rgba(25,39,32,.45)",
    "reason": "Translucent overlay carries no text itself; compositing and underlying/contained dialog relationships require rendered verification."
  },
  {
    "key": "../ui/styles.css |  | .dialog-backdrop | background:rgba(25,39,32,.45) | background with inherited foreground",
    "reason": "Translucent overlay carries no text itself; compositing and underlying/contained dialog relationships require rendered verification."
  },
  {
    "key": "../ui/styles.css |  | .receipt-card | background:#f2f5f0 | background with inherited foreground",
    "reason": "Pale receipt card inherits body ink with explicit muted labels; descendant combinations need rendered verification."
  },
  {
    "key": "../ui/styles.css |  | dialog.confirm-dialog | color:inherit | Error: unsupported colour inherit -> inherit",
    "reason": "Native StaffDialog intentionally inherits surrounding text colour; preserve native dialog and verify computed styles in Opus acceptance."
  },
  {
    "key": "../ui/styles.css |  | dialog.confirm-dialog::backdrop | background:rgba(25, 39, 32, .45) | Error: unsupported colour rgba(25, 39, 32, .45) -> rgba(25, 39, 32, .45)",
    "reason": "Native dialog translucent backdrop has no text; overlay compositing is outside the static grammar."
  },
  {
    "key": "../ui/styles.css |  | dialog.confirm-dialog::backdrop | background:rgba(25, 39, 32, .45) | background with inherited foreground",
    "reason": "Native dialog translucent backdrop has no text; overlay compositing is outside the static grammar."
  },
  {
    "key": "../ui/styles.css |  | .adjustment-preview,.refund-list | background:#f2f5f0 | background with inherited foreground",
    "reason": "Pale adjustment/refund containers inherit body ink; dynamic child content and statuses need rendered verification."
  }
];
