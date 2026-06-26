import React from 'react';
import { Icon, Stack } from '@grafana/ui';
import { templateMeta } from './templateMeta';

// Table cell that renders a PushWard template as an icon plus its label. Shared
// by the Activities and Widgets tables.
export function TemplateCell({ template }: { template?: string }) {
  const m = templateMeta(template);
  return (
    <Stack direction="row" gap={0.5} alignItems="center">
      <Icon name={m.icon} />
      <span>{m.label}</span>
    </Stack>
  );
}
