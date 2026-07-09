import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';

export type StatusTone = 'success' | 'warning' | 'error' | 'neutral';

// The theme color each tone maps to. Single source for StatCard and StatusCard
// so the two Overview grids can't drift out of sync.
export function toneColor(theme: GrafanaTheme2, tone: StatusTone): string {
  switch (tone) {
    case 'success':
      return theme.colors.success.text;
    case 'warning':
      return theme.colors.warning.text;
    case 'error':
      return theme.colors.error.text;
    case 'neutral':
      return theme.colors.text.secondary;
  }
}

// The shared tile surface: a secondary-background card with a tone-colored left
// edge. Callers add the edge color via border-left-color on their own class.
export function tileBase(theme: GrafanaTheme2) {
  return css`
    padding: ${theme.spacing(2)};
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    border-left-width: 3px;
    border-radius: ${theme.shape.radius.default};
  `;
}
