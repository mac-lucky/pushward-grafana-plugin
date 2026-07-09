import React from 'react';
import { css, cx } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Text, useStyles2 } from '@grafana/ui';
import { StatusTone } from './StatusCard';

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

const getStyles = (theme: GrafanaTheme2) => {
  const tone = (color: string) => css`
    border-left-color: ${color};
  `;
  return {
    card: css`
      padding: ${theme.spacing(2)};
      background: ${theme.colors.background.secondary};
      border: 1px solid ${theme.colors.border.weak};
      border-left-width: 3px;
      border-radius: ${theme.shape.radius.default};
    `,
    tone: {
      success: tone(theme.colors.success.text),
      warning: tone(theme.colors.warning.text),
      error: tone(theme.colors.error.text),
      neutral: tone(theme.colors.text.secondary),
    },
  };
};
