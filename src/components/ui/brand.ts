import { config } from '@grafana/runtime';
import { PLUGIN_ID } from '../../constants';

// PushWard accent: iOS systemBlue. Used sparingly for highlights so the rest of
// the UI keeps to Grafana's theme tokens and stays correct in light and dark.
export const BRAND_ACCENT = '#0a84ff';

// Deep navy band behind the Overview hero. White text and the logo sit on top,
// so the hero reads the same in either theme.
export const HERO_BG = 'linear-gradient(135deg, #0d0d26 0%, #1a1a2e 55%, #232350 100%)';

// Absolute URL to a bundled plugin asset, resolved through Grafana's configured
// sub-path so it also works behind a reverse proxy.
export function assetUrl(file: string): string {
  const base = config.appSubUrl ?? '';
  return `${base}/public/plugins/${PLUGIN_ID}/img/${file}`;
}

export const logoSmallUrl = () => assetUrl('logo-small.png');
