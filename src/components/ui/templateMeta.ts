import { type IconName } from '@grafana/ui';

export interface TemplateMeta {
  icon: IconName;
  label: string;
}

// Maps a PushWard template id to an icon and a human label for the Activities
// and Widgets tables. Unknown templates fall back to the raw id.
const META: Record<string, TemplateMeta> = {
  timeline: { icon: 'graph-bar', label: 'Timeline' },
  gauge: { icon: 'tachometer-fast', label: 'Gauge' },
  stat_list: { icon: 'list-ul', label: 'Stat list' },
  value: { icon: 'calculator-alt', label: 'Value' },
  progress: { icon: 'percentage', label: 'Progress' },
  status: { icon: 'heart-rate', label: 'Status' },
  countdown: { icon: 'clock-nine', label: 'Countdown' },
};

export function templateMeta(template?: string): TemplateMeta {
  if (!template) {
    return { icon: 'apps', label: '-' };
  }
  return META[template] ?? { icon: 'apps', label: template };
}
