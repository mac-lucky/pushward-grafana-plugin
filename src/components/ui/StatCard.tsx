import React from 'react';
import { css, cx } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Text, useStyles2 } from '@grafana/ui';
import { StatusTone, tileBase, toneColor } from './cardStyle';

export interface StatCardProps {
  tone: StatusTone;
  value: number;
  label: string;
}

// A counter tile for the Overview delivery grid. It mirrors StatusCard's tone
// edge so the two grids read as one family, but leads with the numeral instead
// of a title, which is what a KPI needs.
export function StatCard({ tone, value, label }: StatCardProps) {
  const s = useStyles2(getStyles);
  return (
    <div className={cx(s.card, s.tone[tone])}>
      <Text element="p" variant="h2">
        {value.toLocaleString()}
      </Text>
      <Text variant="bodySmall" color="secondary">
        {label}
      </Text>
    </div>
  );
}

const getStyles = (theme: GrafanaTheme2) => ({
  card: tileBase(theme),
  tone: {
    success: css`border-left-color: ${toneColor(theme, 'success')};`,
    warning: css`border-left-color: ${toneColor(theme, 'warning')};`,
    error: css`border-left-color: ${toneColor(theme, 'error')};`,
    neutral: css`border-left-color: ${toneColor(theme, 'neutral')};`,
  },
});
