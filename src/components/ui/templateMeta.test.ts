import { creatableTemplates, templateMeta } from './templateMeta';

describe('templateMeta', () => {
  it("falls back to the apps icon and '-' label for empty input", () => {
    expect(templateMeta(undefined)).toEqual({ icon: 'apps', label: '-' });
    expect(templateMeta('')).toEqual({ icon: 'apps', label: '-' });
  });

  it('returns the raw id as the label for unknown templates', () => {
    expect(templateMeta('mystery')).toEqual({ icon: 'apps', label: 'mystery' });
  });

  it('maps known templates to their icon and label', () => {
    expect(templateMeta('gauge')).toMatchObject({ icon: 'tachometer-fast', label: 'Gauge' });
    expect(templateMeta('stat_list')).toMatchObject({ icon: 'list-ul', label: 'Stat list' });
    expect(templateMeta('timeline')).toMatchObject({ icon: 'graph-bar', label: 'Timeline' });
    expect(templateMeta('trend')).toMatchObject({ icon: 'chart-line', label: 'Trend' });
  });

  // The plugin cannot create these, but the Widgets table lists whatever is on
  // the account, so a widget made elsewhere must not render as a raw id.
  it('labels templates the plugin does not create', () => {
    expect(templateMeta('battery').label).toBe('Battery');
    expect(templateMeta('schedule').label).toBe('Schedule');
    expect(templateMeta('flow').label).toBe('Flow');
  });

  // The Activities table renders through this same map off a raw GET
  // /activities, so every activity template needs a row or it shows as the raw
  // id with the unknown-template icon.
  it('labels the activity templates the Activities table lists', () => {
    expect(templateMeta('generic').label).toBe('Generic');
    expect(templateMeta('steps').label).toBe('Steps');
    expect(templateMeta('alert').label).toBe('Alert');
    expect(templateMeta('board').label).toBe('Board');
    expect(templateMeta('log').label).toBe('Log');
    expect(templateMeta('media').label).toBe('Media');
    expect(templateMeta('approval').label).toBe('Approval');
  });

  // 'apps' is the unknown-template fallback, so reusing it would make a known
  // template indistinguishable from an unrecognised one.
  it('does not reuse the unknown-template icon for a known template', () => {
    for (const id of ['generic', 'steps', 'alert', 'board', 'log', 'media', 'approval']) {
      expect(templateMeta(id).icon).not.toBe('apps');
    }
  });
});

describe('creatableTemplates', () => {
  // This list drives the widget builder's picker. It has to match the Go
  // validator's validWidgetTemplates exactly, or the form offers a template the
  // backend rejects and the bad entry disables the whole widget engine.
  it('offers exactly the templates the widget engine can build', () => {
    expect(creatableTemplates().map((o) => o.value)).toEqual([
      'value',
      'progress',
      'status',
      'gauge',
      'stat_list',
      'trend',
      'countdown',
    ]);
  });

  it('excludes the templates the plugin cannot poll for', () => {
    const offered = creatableTemplates().map((o) => o.value);
    const excluded = ['timeline', 'battery', 'schedule', 'flow', 'generic', 'steps', 'alert', 'board', 'log', 'media', 'approval'];
    for (const id of excluded) {
      expect(offered).not.toContain(id);
    }
  });

  it('carries the same labels the tables use', () => {
    for (const { label, value } of creatableTemplates()) {
      expect(label).toBe(templateMeta(value).label);
    }
  });
});
