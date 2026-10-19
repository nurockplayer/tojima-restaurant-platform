import { useEffect, useRef, useState } from 'react';
import type { GuestMenu } from '../../api/types';
import styles from './CategoryNav.module.css';

type Locale = 'ja' | 'zh';

export interface CategoryNavProps {
  categories: GuestMenu['categories'];
  currentCategoryKey?: string;
  locale: Locale;
  onNavigate?: (categoryKey: string) => void;
}

const ja = { label: 'カテゴリ', empty: 'カテゴリはありません' };
const copy: Record<Locale, Record<keyof typeof ja, string>> = {
  ja,
  zh: { label: '分類', empty: '目前沒有分類' },
};

export function categoryHeadingId(categoryKey: string) {
  return `guest-category-${encodeURIComponent(categoryKey)}`;
}

export function CategoryNav({ categories, currentCategoryKey, locale, onNavigate }: CategoryNavProps) {
  const strings = copy[locale];
  const [observedCategoryKey, setObservedCategoryKey] = useState(categories[0]?.key ?? '');
  const nav = useRef<HTMLElement | null>(null);
  const activeCategoryKey = currentCategoryKey ?? observedCategoryKey;

  useEffect(() => {
    if (currentCategoryKey !== undefined || categories.length < 2) return;
    const updateCurrentCategory = () => {
      const threshold = Math.max(nav.current?.getBoundingClientRect().bottom ?? 0, window.innerHeight * 0.25);
      const passed = categories.filter((category) => {
        const heading = document.getElementById(categoryHeadingId(category.key));
        return heading instanceof HTMLElement && heading.getBoundingClientRect().top <= threshold;
      });
      setObservedCategoryKey(passed.at(-1)?.key ?? categories[0]!.key);
    };
    window.addEventListener('scroll', updateCurrentCategory, { passive: true });
    window.addEventListener('resize', updateCurrentCategory);
    updateCurrentCategory();
    return () => {
      window.removeEventListener('scroll', updateCurrentCategory);
      window.removeEventListener('resize', updateCurrentCategory);
    };
  }, [categories, currentCategoryKey]);

  if (categories.length === 0) return <nav aria-label={strings.label} className={styles.nav} data-category-nav><span>{strings.empty}</span></nav>;

  const navigate = (categoryKey: string) => {
    setObservedCategoryKey(categoryKey);
    const heading = document.getElementById(categoryHeadingId(categoryKey));
    if (heading instanceof HTMLElement) {
      heading.scrollIntoView({ block: 'start' });
      heading.focus({ preventScroll: true });
    }
    onNavigate?.(categoryKey);
  };

  return <nav aria-label={strings.label} className={styles.nav} data-category-nav ref={nav}>
    <div className={styles.items}>
      {categories.map((category) => <button
        aria-current={activeCategoryKey === category.key ? 'location' : undefined}
        className={styles.item}
        data-category-key={category.key}
        key={category.key}
        onClick={() => navigate(category.key)}
        type="button"
      >{category.name}</button>)}
    </div>
  </nav>;
}
