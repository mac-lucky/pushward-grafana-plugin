import React from 'react';
import { Tooltip } from '@grafana/ui';
import { formatRfc3339, formatUnix, relativeTime } from '../../dates';

// Table cell showing a relative "n minutes ago" time with the absolute
// timestamp in a tooltip. Accepts an RFC3339 string or a unix timestamp. The
// tooltip is omitted for empty values so a bare "-" cell does not pop one.
export function RelativeTimeCell({ value }: { value: string | number | undefined }) {
  const relative = relativeTime(value);
  if (relative === '-') {
    return <span>-</span>;
  }
  const absolute = typeof value === 'number' ? formatUnix(value) : formatRfc3339(value);
  return (
    <Tooltip content={absolute}>
      <span>{relative}</span>
    </Tooltip>
  );
}
