import type { GuestMenu } from '../../api/types';
import { amountJPY } from '../../api/client';
import { categoryHeadingId } from './CategoryNav';
import styles from './MenuList.module.css';

type Locale = 'ja' | 'zh';
export type GuestProduct = GuestMenu['categories'][number]['products'][number];
export interface LicensedMenuMedia { src: string; alt: string; licensed: true }

export function isMenuAmountSafe(amount: string | number): boolean {
  return typeof amount === 'number' ? Number.isSafeInteger(amount) : /^-?\d+$/.test(amount);
}

function productAmountsSafe(product: GuestProduct): boolean {
  return isMenuAmountSafe(product.effective_price_minor) && product.option_groups.every((group) => group.options.every((option) => isMenuAmountSafe(option.price_delta_minor)));
}

export interface MenuListProps {
  categories: GuestMenu['categories'];
  locale: Locale;
  mediaByProduct?: Record<string, LicensedMenuMedia | undefined>;
  busy?: boolean;
  uncertain?: boolean;
  onAdd?: (product: GuestProduct) => void;
  onOpenOptions?: (product: GuestProduct) => void;
}

const ja = {
  menu: 'メニュー', available: '注文できます', unavailable: '現在注文できません',
  pending: '店舗確認中', emergency: '販売停止中', add: 'カートに追加',
  amountError: '金額を安全に表示できません。注文を続けられません。',
};
const copy: Record<Locale, Record<keyof typeof ja, string>> = {
  ja,
  zh: { menu: '菜單', available: '可點選', unavailable: '目前無法點選', pending: '門店確認中', emergency: '暫停販售', add: '加入購物車', amountError: '無法安全顯示金額，暫時不能繼續送單。' },
};

export function MenuList({ categories, locale, mediaByProduct = {}, busy = false, uncertain = false, onAdd, onOpenOptions }: MenuListProps) {
  const strings = copy[locale];
  return <div className={styles.menu} aria-label={strings.menu}>
    {categories.map((category) => <section aria-labelledby={categoryHeadingId(category.key)} className={styles.category} data-category-section data-category-key={category.key} key={category.key}>
      <h2 id={categoryHeadingId(category.key)} data-category-key={category.key} tabIndex={-1} className={styles.categoryHeading}>{category.name}</h2>
      <div className={styles.rows}>
        {category.products.map((product) => {
          const held = product.company_emergency_hold_active === true;
          const sellable = product.selectable && !held;
          const amountSafe = productAmountsSafe(product);
          const media = mediaByProduct[product.key];
          const status = held ? strings.emergency : product.selectable ? strings.available : product.availability === 'PENDING_CONFIRMATION' ? strings.pending : strings.unavailable;
          return <article aria-label={product.name} className={styles.row} data-has-media={media?.licensed === true ? '' : undefined} data-product-key={product.key} key={product.key}>
            {media?.licensed === true && <div className={styles.media} data-menu-media><img src={media.src} alt={media.alt} /></div>}
            <div className={styles.details}>
              <div className={styles.productHeading}><h3>{product.name}</h3><strong>{amountJPY(product.effective_price_minor)}</strong></div>
              {product.description && <p className={styles.description}>{product.description}</p>}
              {(!sellable || amountSafe) && <p className={sellable ? styles.available : styles.unavailable}>{status}</p>}
              {!amountSafe && <p className={styles.unavailable} role="alert">{strings.amountError}</p>}
            </div>
            <div className={styles.actions}>
              <button
                aria-label={strings.add}
                className={styles.add}
                data-add-product={product.key}
                disabled={busy || uncertain || !sellable || !amountSafe}
                onClick={() => product.option_groups.length > 0 ? onOpenOptions?.(product) : onAdd?.(product)}
                type="button"
              >＋</button>
            </div>
          </article>;
        })}
      </div>
    </section>)}
  </div>;
}
