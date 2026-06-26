import React from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { useStyles2 } from '@grafana/ui';
import { HERO_BG, logoSmallUrl } from './brand';

interface PageHeroProps {
  title: string;
  tagline: React.ReactNode;
}

// Branded banner for the landing page. The background is always dark, so the
// text and logo are forced white rather than read from the theme. The title is
// a plain div, not a heading: PluginPage already provides the page's h1, and the
// hero is decorative branding rather than a document section.
export function PageHero({ title, tagline }: PageHeroProps) {
  const s = useStyles2(getStyles);
  return (
    <div className={s.hero}>
      <img className={s.logo} src={logoSmallUrl()} alt="" width={56} height={56} />
      <div>
        <div className={s.title}>{title}</div>
        <div className={s.tagline}>{tagline}</div>
      </div>
    </div>
  );
}

const getStyles = (theme: GrafanaTheme2) => ({
  hero: css`
    display: flex;
    align-items: center;
    gap: ${theme.spacing(2.5)};
    padding: ${theme.spacing(3, 4)};
    margin-bottom: ${theme.spacing(3)};
    border-radius: ${theme.shape.radius.default};
    background: ${HERO_BG};
    box-shadow: ${theme.shadows.z2};
  `,
  logo: css`
    flex: 0 0 auto;
    border-radius: ${theme.shape.radius.default};
  `,
  title: css`
    margin: 0;
    color: #ffffff;
    font-size: ${theme.typography.h1.fontSize};
    font-weight: ${theme.typography.fontWeightBold};
    line-height: ${theme.typography.h1.lineHeight};
  `,
  tagline: css`
    margin-top: ${theme.spacing(0.5)};
    color: rgba(255, 255, 255, 0.85);
    font-size: ${theme.typography.h5.fontSize};
  `,
});
