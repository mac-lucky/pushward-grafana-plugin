import React from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Button, Combobox, Field, IconButton, Input, Stack, Text, useStyles2, type ComboboxOption } from '@grafana/ui';
import type { WidgetConfig } from './AppConfig';

// A row of a stat_list widget (subset the form edits; unknown fields ride along).
type StatRow = { label?: string; query?: string; value_template?: string; unit?: string; [k: string]: unknown };
type WidgetContent = { [k: string]: unknown };

const TEMPLATE_OPTIONS: Array<ComboboxOption<string>> = [
  { label: 'Value', value: 'value' },
  { label: 'Progress', value: 'progress' },
  { label: 'Status', value: 'status' },
  { label: 'Gauge', value: 'gauge' },
  { label: 'Stat list', value: 'stat_list' },
];

const MODE_OPTIONS: Array<ComboboxOption<string>> = [
  { label: 'On change', value: 'on_change' },
  { label: 'Always', value: 'always' },
];

const W = 36;
const NUM_W = 18;

function content(w: WidgetConfig): WidgetContent {
  return (w.content as WidgetContent) ?? {};
}
function rows(w: WidgetConfig): StatRow[] {
  return Array.isArray(w.stat_rows) ? (w.stat_rows as StatRow[]) : [];
}
// numOrUndef keeps an empty input as "no key" (so JSON.stringify drops it),
// rather than coercing blanks to 0 which would change the server-side meaning.
function numOrUndef(v: string): number | undefined {
  const t = v.trim();
  if (t === '') {
    return undefined;
  }
  const n = Number(t);
  return Number.isNaN(n) ? undefined : n;
}

export interface WidgetBuilderProps {
  widgets: WidgetConfig[];
  onChange: (widgets: WidgetConfig[]) => void;
}

// WidgetBuilder is a form over the jsonData.widgets array. It edits the common
// fields directly; fields it doesn't render (query_all fan-out, templates,
// content colours) are preserved on each widget object so round-tripping an
// advanced widget through the form is lossless. The backend remains the
// authority on validation - this form only shapes the same JSON.
export function WidgetBuilder({ widgets, onChange }: WidgetBuilderProps) {
  const s = useStyles2(getStyles);

  const update = (i: number, patch: Partial<WidgetConfig>) =>
    onChange(widgets.map((w, idx) => (idx === i ? { ...w, ...patch } : w)));

  const updateContent = (i: number, patch: WidgetContent) =>
    update(i, { content: { ...content(widgets[i]), ...patch } });

  const updateRow = (i: number, j: number, patch: StatRow) =>
    update(i, { stat_rows: rows(widgets[i]).map((r, idx) => (idx === j ? { ...r, ...patch } : r)) });

  const addRow = (i: number) =>
    update(i, { stat_rows: [...rows(widgets[i]), { label: '', query: '', value_template: '{{ .Value }}', unit: '' }] });

  const removeRow = (i: number, j: number) =>
    update(i, { stat_rows: rows(widgets[i]).filter((_, idx) => idx !== j) });

  // changeTemplate switches a widget's template AND clears the fields that the
  // new template forbids, so the Go validator (which rejects query/query_all on
  // stat_list, and stat_rows on scalar templates, and fails the WHOLE array on
  // one bad entry) never disables the engine after a template change.
  const changeTemplate = (i: number, template: string) => {
    const w = widgets[i];
    const patch: Partial<WidgetConfig> = { template };
    if (template === 'stat_list') {
      patch.query = undefined;
      patch.query_all = undefined;
      patch.slug_template = undefined;
      if (rows(w).length === 0) {
        patch.stat_rows = [{ label: '', query: '', value_template: '{{ .Value }}', unit: '' }];
      }
    } else {
      patch.stat_rows = undefined;
      if (typeof w.query !== 'string' && typeof w.query_all !== 'string') {
        patch.query = '';
      }
    }
    update(i, patch);
  };

  const addWidget = () =>
    onChange([...widgets, { slug: '', name: '', template: 'value', query: '', interval: '60s', update_mode: 'on_change' }]);

  const removeWidget = (i: number) => onChange(widgets.filter((_, idx) => idx !== i));

  if (widgets.length === 0) {
    return (
      <div className={s.empty}>
        <Text color="secondary">No widgets configured. Add one to publish a PromQL-backed iOS widget.</Text>
        <div className={s.marginTop}>
          <Button type="button" variant="secondary" icon="plus" onClick={addWidget}>
            Add widget
          </Button>
        </div>
      </div>
    );
  }

  return (
    <Stack direction="column" gap={2}>
      {widgets.map((w, i) => {
        const c = content(w);
        const isStatList = w.template === 'stat_list';
        const isRanged = w.template === 'progress' || w.template === 'gauge';
        const isFanout = typeof w.query_all === 'string' && w.query_all !== '';
        return (
          <div key={i} className={s.card}>
            <div className={s.cardHeader}>
              <Text element="h4" variant="h6">
                {w.name || w.slug || `Widget ${i + 1}`}
              </Text>
              <IconButton name="trash-alt" tooltip="Remove widget" aria-label="Remove widget" onClick={() => removeWidget(i)} />
            </div>

            <Stack direction="row" gap={2} wrap="wrap">
              <Field label="Slug" description="a-z0-9_- , unique">
                <Input width={W} value={w.slug ?? ''} placeholder="my-widget" onChange={(e) => update(i, { slug: e.currentTarget.value })} />
              </Field>
              <Field label="Name">
                <Input width={W} value={w.name ?? ''} placeholder={w.slug} onChange={(e) => update(i, { name: e.currentTarget.value })} />
              </Field>
              <Field label="Template">
                <Combobox
                  width={W}
                  options={TEMPLATE_OPTIONS}
                  value={(w.template as string) ?? 'value'}
                  onChange={(opt: ComboboxOption<string>) => changeTemplate(i, opt.value)}
                />
              </Field>
            </Stack>

            {!isStatList &&
              (isFanout ? (
                <Field label="Query (multi-series)" description="Edit query_all / slug_template fan-out in the raw JSON view.">
                  <Input width={W * 2} disabled value={w.query_all as string} />
                </Field>
              ) : (
                <Field label="Query" description="PromQL / MetricsQL returning a scalar.">
                  <Input
                    width={W * 2}
                    value={(w.query as string) ?? ''}
                    placeholder="count(up == 1)"
                    onChange={(e) => update(i, { query: e.currentTarget.value })}
                  />
                </Field>
              ))}

            <Stack direction="row" gap={2} wrap="wrap">
              <Field label="Interval" description="Go duration, min 5s">
                <Input width={NUM_W} value={(w.interval as string) ?? ''} placeholder="60s" onChange={(e) => update(i, { interval: e.currentTarget.value })} />
              </Field>
              <Field label="Update mode">
                <Combobox
                  width={NUM_W}
                  options={MODE_OPTIONS}
                  value={(w.update_mode as string) ?? 'on_change'}
                  onChange={(opt: ComboboxOption<string>) => update(i, { update_mode: opt.value })}
                />
              </Field>
              <Field label="Unit">
                <Input width={NUM_W} value={(c.unit as string) ?? ''} placeholder="req/s" onChange={(e) => updateContent(i, { unit: e.currentTarget.value })} />
              </Field>
              <Field label="Icon" description="SF Symbol name">
                <Input width={NUM_W} value={(c.icon as string) ?? ''} placeholder="chart.bar.fill" onChange={(e) => updateContent(i, { icon: e.currentTarget.value })} />
              </Field>
            </Stack>

            {isRanged && (
              <Stack direction="row" gap={2} wrap="wrap">
                <Field label="Min value" description="Required for progress/gauge">
                  <Input
                    width={NUM_W}
                    type="number"
                    value={c.min_value === undefined ? '' : String(c.min_value)}
                    onChange={(e) => updateContent(i, { min_value: numOrUndef(e.currentTarget.value) })}
                  />
                </Field>
                <Field label="Max value" description="Required for progress/gauge">
                  <Input
                    width={NUM_W}
                    type="number"
                    value={c.max_value === undefined ? '' : String(c.max_value)}
                    onChange={(e) => updateContent(i, { max_value: numOrUndef(e.currentTarget.value) })}
                  />
                </Field>
              </Stack>
            )}

            {isStatList && (
              <div className={s.rows}>
                <Text variant="bodySmall" color="secondary">
                  Rows (1-6) - each polls its own query
                </Text>
                {rows(w).map((r, j) => (
                  <Stack key={j} direction="row" gap={1} alignItems="flex-end" wrap="wrap">
                    <Field label="Label">
                      <Input width={NUM_W} value={r.label ?? ''} onChange={(e) => updateRow(i, j, { label: e.currentTarget.value })} />
                    </Field>
                    <Field label="Query">
                      <Input width={W} value={r.query ?? ''} onChange={(e) => updateRow(i, j, { query: e.currentTarget.value })} />
                    </Field>
                    <Field label="Value template">
                      <Input
                        width={NUM_W}
                        value={r.value_template ?? ''}
                        placeholder="{{ .Value }}"
                        onChange={(e) => updateRow(i, j, { value_template: e.currentTarget.value })}
                      />
                    </Field>
                    <Field label="Unit">
                      <Input width={12} value={r.unit ?? ''} onChange={(e) => updateRow(i, j, { unit: e.currentTarget.value })} />
                    </Field>
                    <IconButton name="trash-alt" tooltip="Remove row" aria-label="Remove row" onClick={() => removeRow(i, j)} />
                  </Stack>
                ))}
                <div className={s.marginTop}>
                  <Button type="button" size="sm" variant="secondary" icon="plus" onClick={() => addRow(i)}>
                    Add row
                  </Button>
                </div>
              </div>
            )}
          </div>
        );
      })}

      <div>
        <Button type="button" variant="secondary" icon="plus" onClick={addWidget}>
          Add widget
        </Button>
      </div>
    </Stack>
  );
}

const getStyles = (theme: GrafanaTheme2) => ({
  empty: css`
    padding: ${theme.spacing(2)};
    border: 1px dashed ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
  `,
  card: css`
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
    padding: ${theme.spacing(2)};
  `,
  cardHeader: css`
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: ${theme.spacing(1)};
  `,
  rows: css`
    margin-top: ${theme.spacing(1)};
    padding-top: ${theme.spacing(1)};
    border-top: 1px solid ${theme.colors.border.weak};
  `,
  marginTop: css`
    margin-top: ${theme.spacing(1)};
  `,
});
