import React from 'react';
import { css, cx } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Icon, type IconName, Text, useStyles2 } from '@grafana/ui';
import { StatusTone, tileBase, toneColor } from './cardStyle';

export type { StatusTone };

export interface StatusCardProps {
  tone: StatusTone;
  icon: IconName;
  title: string;
  description?: React.ReactNode;
  action?: { label: string; href: string };
}

// A static status tile used in the Overview status grid. Clickable navigation
// uses Grafana's Card elsewhere; this stays a plain tile so it does not read as
// interactive, with a tone-colored left edge and icon for quick scanning.
export function StatusCard({ tone, icon, title, description, action }: StatusCardProps) {
  const s = useStyles2(getStyles);
  const t = s.tone[tone];
  return (
    <div className={cx(s.card, t.border)}>
      <Icon name={icon} size="xl" className={t.icon} />
      <div className={s.body}>
        <Text variant="body" weight="medium">
          {title}
        </Text>
        {description != null && (
          <div className={s.desc}>
            <Text variant="bodySmall" color="secondary">
              {description}
            </Text>
          </div>
        )}
        {action && (
          <a href={action.href} className={s.action}>
            {action.label}
          </a>
        )}
      </div>
    </div>
  );
}

const getStyles = (theme: GrafanaTheme2) => {
  const tone = (t: StatusTone) => {
    const color = toneColor(theme, t);
    return {
      border: css`
        border-left-color: ${color};
      `,
      icon: css`
        color: ${color};
        flex: 0 0 auto;
      `,
    };
  };
  return {
    card: cx(
      tileBase(theme),
      css`
        display: flex;
        gap: ${theme.spacing(1.5)};
        align-items: flex-start;
        height: 100%;
      `
    ),
    body: css`
      min-width: 0;
    `,
    desc: css`
      margin-top: ${theme.spacing(0.25)};
    `,
    action: css`
      display: inline-block;
      margin-top: ${theme.spacing(1)};
      color: ${theme.colors.text.link};
      font-size: ${theme.typography.bodySmall.fontSize};
      &:hover {
        text-decoration: underline;
      }
    `,
    tone: {
      success: tone('success'),
      warning: tone('warning'),
      error: tone('error'),
      neutral: tone('neutral'),
    },
  };
};
