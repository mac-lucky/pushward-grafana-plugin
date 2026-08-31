import { type IconName } from '@grafana/ui';

export interface TemplateMeta {
  icon: IconName;
  label: string;
  /**
   * Whether the widget engine can build this template from a PromQL poll.
   * Non-creatable entries still need a row: the Widgets table lists everything
   * on the account, so a widget made through the REST API or Home Assistant has
   * to read properly here even though this plugin could not have created it.
   * `timeline`, `generic`, `steps`, `alert`, `board`, `log`, `media` and
   * `approval` are activity templates rather than widget ones; they are here
   * because the Activities table renders through the same map. `gauge` and
   * `countdown` are both.
   */
  creatable?: boolean;
}

// Maps a PushWard template id to an icon and a human label for the Activities
// and Widgets tables, and doubles as the source for the widget builder's
// template picker. Unknown templates fall back to the raw id.
//
// Order matters: the picker renders creatable entries in this order.
const META: Record<string, TemplateMeta> = {
  value: { icon: 'calculator-alt', label: 'Value', creatable: true },
  progress: { icon: 'percentage', label: 'Progress', creatable: true },
  status: { icon: 'heart-rate', label: 'Status', creatable: true },
  gauge: { icon: 'tachometer-fast', label: 'Gauge', creatable: true },
  stat_list: { icon: 'list-ul', label: 'Stat list', creatable: true },
  trend: { icon: 'chart-line', label: 'Trend', creatable: true },
  countdown: { icon: 'clock-nine', label: 'Countdown', creatable: true },
  timeline: { icon: 'graph-bar', label: 'Timeline' },
  battery: { icon: 'bolt', label: 'Battery' },
  schedule: { icon: 'calendar-alt', label: 'Schedule' },
  flow: { icon: 'sitemap', label: 'Flow' },
  generic: { icon: 'gf-layout-simple', label: 'Generic' },
  steps: { icon: 'process', label: 'Steps' },
  alert: { icon: 'exclamation-triangle', label: 'Alert' },
  board: { icon: 'table', label: 'Board' },
  log: { icon: 'file-alt', label: 'Log' },
  media: { icon: 'play', label: 'Media' },
  approval: { icon: 'question-circle', label: 'Approval' },
};

export function templateMeta(template?: string): TemplateMeta {
  if (!template) {
    return { icon: 'apps', label: '-' };
  }
  return META[template] ?? { icon: 'apps', label: template };
}

/** The templates the widget builder offers, as {label, value} picker options. */
export function creatableTemplates(): Array<{ label: string; value: string }> {
  return Object.entries(META)
    .filter(([, meta]) => meta.creatable)
    .map(([value, meta]) => ({ label: meta.label, value }));
}
