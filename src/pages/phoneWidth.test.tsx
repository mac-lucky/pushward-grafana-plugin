import React from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import Activities from './Activities';
import Widgets from './Widgets';

const REFUSED = 'pushward: 422 value must be between min_value and max_value';

const responses: Record<string, unknown> = {
  '/widgets': {
    widgets: [{ slug: 'web-cpu', name: 'Web CPU', content: { template: 'gauge', value: 88.42, unit: '%' } }],
  },
  '/activities': { activities: [{ slug: 'grafana-0123456789abcdef', name: 'HighCPU', state: 'ongoing', priority: 5 }] },
  '/history': {
    entries: [
      { ts: 1, alertname: 'HighCPU', slug: 'grafana-0123456789abcdef', action: 'firing', ok: true, detail: 'sent' },
      { ts: 2, alertname: 'HighCPU', slug: 'grafana-0123456789abcdef', action: 'firing', ok: false, detail: REFUSED },
    ],
  },
  '/active': { active: [] },
};

jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  PluginPage: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  getBackendSrv: () => ({
    get: (url: string) => Promise.resolve(responses[url.slice(url.lastIndexOf('/'))]),
  }),
}));

// jest-setup answers every media query with false, which is a desktop screen.
const desktopMatchMedia = window.matchMedia;
function phoneWidth() {
  window.matchMedia = (query: string) => ({ ...desktopMatchMedia(query), matches: query.includes('max-width') });
}
afterEach(() => {
  window.matchMedia = desktopMatchMedia;
});

async function headers(table: number) {
  const tables = await screen.findAllByRole('table');
  return Array.from(tables[table].querySelectorAll('th'), (th) => th.textContent);
}

describe('Widgets table', () => {
  test('shows every column on a desktop', async () => {
    render(<Widgets />);
    expect(await headers(0)).toEqual(['Name', 'Slug', 'Template', 'Value', 'Unit', 'Updated']);
  });

  test('leaves out the slug on a phone', async () => {
    phoneWidth();
    render(<Widgets />);
    expect(await headers(0)).toEqual(['Name', 'Template', 'Value', 'Unit', 'Updated']);
    expect(screen.getByText('Web CPU')).toBeInTheDocument();
    expect(screen.getByText('88.42')).toBeInTheDocument();
  });
});

describe('Activities tables', () => {
  test('show every column on a desktop', async () => {
    render(<Activities />);
    expect(await headers(0)).toEqual(['Name', 'Slug', 'State', 'Template', 'Priority', 'Updated', 'Actions']);
    expect(await headers(1)).toEqual(['Time', 'Alert', 'Action', 'Activity', 'Result', 'Detail']);
  });

  test('keep the name, state and actions on a phone', async () => {
    phoneWidth();
    render(<Activities />);
    expect(await headers(0)).toEqual(['Name', 'State', 'Actions']);
    expect(await headers(1)).toEqual(['Time', 'Alert', 'Action', 'Result']);
    // Silence and End share one cell; on a narrow screen End goes under Silence.
    expect(screen.getByRole('button', { name: 'End' }).parentElement).toHaveStyle({ flexWrap: 'wrap' });
  });

  // Without the Detail column the result badge is the only way to read why a
  // delivery failed.
  test('show a failed delivery reason from its result on a phone', async () => {
    phoneWidth();
    render(<Activities />);
    const failed = await screen.findByText('failed');
    expect(screen.queryByText(REFUSED)).not.toBeInTheDocument();
    fireEvent.mouseEnter(failed);
    expect(await screen.findByRole('tooltip')).toHaveTextContent(REFUSED);
  });
});
