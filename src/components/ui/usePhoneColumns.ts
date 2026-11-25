import { useEffect, useMemo, useState } from 'react';
import { useTheme2, type Column } from '@grafana/ui';

// usePhoneColumns leaves the secondary columns out below Grafana's sm
// breakpoint, so on a phone the columns that matter fit the screen instead of
// the table scrolling its right-hand ones out of sight. Wider screens get
// every column. secondary must be a module constant.
export function usePhoneColumns<T extends object>(columns: Array<Column<T>>, secondary: string[]): Array<Column<T>> {
  const query = `(max-width: ${useTheme2().breakpoints.values.sm - 1}px)`;
  const [phone, setPhone] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const mq = window.matchMedia(query);
    const onChange = () => setPhone(mq.matches);
    onChange();
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, [query]);
  return useMemo(
    () => (phone ? columns.filter((c) => !secondary.includes(String(c.id))) : columns),
    [phone, columns, secondary]
  );
}
