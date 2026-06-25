import { dateTimeFormat } from '@grafana/data';

// formatRfc3339 renders an RFC3339 timestamp string the same way across the
// activity and widget tables: '-' for an empty value, the raw string back if it
// cannot be parsed, otherwise Grafana's configured date/time format.
export function formatRfc3339(value: string | undefined): string {
  if (!value) {
    return '-';
  }
  const ms = new Date(value).getTime();
  return Number.isNaN(ms) ? value : dateTimeFormat(ms);
}
