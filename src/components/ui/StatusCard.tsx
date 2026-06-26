import React from 'react';
import { css, cx } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Icon, type IconName, Text, useStyles2 } from '@grafana/ui';

export type StatusTone = 'success' | 'warning' | 'error' | 'neutral';

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
  const tone = (color: string) => ({
    border: css`
      border-left-color: ${color};
    `,
    icon: css`
      color: ${color};
      flex: 0 0 auto;
    `,
  });
  return {
    card: css`
      display: flex;
      gap: ${theme.spacing(1.5)};
      align-items: flex-start;
      height: 100%;
      padding: ${theme.spacing(2)};
      background: ${theme.colors.background.secondary};
      border: 1px solid ${theme.colors.border.weak};
      border-left-width: 3px;
      border-radius: ${theme.shape.radius.default};
    `,
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
      success: tone(theme.colors.success.text),
      warning: tone(theme.colors.warning.text),
      error: tone(theme.colors.error.text),
      neutral: tone(theme.colors.text.secondary),
    },
  };
};
