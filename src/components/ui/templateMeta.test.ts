import { templateMeta } from './templateMeta';

describe('templateMeta', () => {
  it("falls back to the apps icon and '-' label for empty input", () => {
    expect(templateMeta(undefined)).toEqual({ icon: 'apps', label: '-' });
    expect(templateMeta('')).toEqual({ icon: 'apps', label: '-' });
  });

  it('returns the raw id as the label for unknown templates', () => {
    expect(templateMeta('mystery')).toEqual({ icon: 'apps', label: 'mystery' });
  });

  it('maps known templates to their icon and label', () => {
    expect(templateMeta('gauge')).toEqual({ icon: 'tachometer-fast', label: 'Gauge' });
    expect(templateMeta('stat_list')).toEqual({ icon: 'list-ul', label: 'Stat list' });
    expect(templateMeta('timeline')).toEqual({ icon: 'graph-bar', label: 'Timeline' });
  });
});
