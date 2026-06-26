import { dateTime, dateTimeFormat } from '@grafana/data';

// toMillis coerces an RFC3339 string or a unix timestamp (seconds or
// milliseconds) to epoch milliseconds. Returns NaN for empty or unparseable
// input. The 1e12 threshold disambiguates seconds (~1.7e9) from ms (~1.7e12).
function toMillis(value: string | number | undefined): number {
  if (value === undefined || value === '' || value === 0) {
    return NaN;
  }
  if (typeof value === 'number') {
    return value < 1e12 ? value * 1000 : value;
  }
  return new Date(value).getTime();
}

// formatRfc3339 renders an RFC3339 timestamp string the same way across the
// activity and widget tables: '-' for an empty value, the raw string back if it
// cannot be parsed, otherwise Grafana's configured date/time format.
export function formatRfc3339(value: string | undefined): string {
  const ms = toMillis(value);
  if (Number.isNaN(ms)) {
    return value ? value : '-';
  }
  return dateTimeFormat(ms);
}

// formatUnix renders a unix timestamp (seconds or milliseconds) as Grafana's
// configured absolute date/time, used as the tooltip behind a relative time.
export function formatUnix(ts: number | undefined): string {
  const ms = toMillis(ts);
  return Number.isNaN(ms) ? '-' : dateTimeFormat(ms);
}

// relativeTime renders a human "n minutes ago" string for either an RFC3339
// string or a unix timestamp (seconds or milliseconds). Falls back to '-' for
// empty input and to the raw string if it cannot be parsed.
export function relativeTime(value: string | number | undefined): string {
  const ms = toMillis(value);
  if (Number.isNaN(ms)) {
    return typeof value === 'string' && value !== '' ? value : '-';
  }
  return dateTime(ms).fromNow();
}
