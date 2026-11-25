import React from 'react';
import { fireEvent, render, screen, within } from '@testing-library/react';
import type { ComboboxOption } from '@grafana/ui';
import { WidgetBuilder } from './WidgetBuilder';
import type { WidgetConfig } from './AppConfig';

// Combobox draws its menu through a virtualiser that renders no rows in jsdom,
// so a native select stands in for it. It takes the id from the surrounding
// Field the way Combobox does, so the field label still finds it.
jest.mock('@grafana/ui', () => {
  const actual = jest.requireActual('@grafana/ui');
  return {
    ...actual,
    Combobox: ({
      options,
      value,
      onChange,
    }: {
      options: Array<ComboboxOption<string>>;
      value: string;
      onChange: (o: ComboboxOption<string>) => void;
    }) => (
      <select
        id={actual.useFieldContext().id}
        value={value}
        onChange={(e) => onChange(options.find((o) => o.value === e.currentTarget.value)!)}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    ),
  };
});

function setup(widgets: WidgetConfig[]) {
  const onChange = jest.fn();
  render(<WidgetBuilder widgets={widgets} onChange={onChange} />);
  // The widgets the form would save, as the JSON the backend reads.
  const saved = (): WidgetConfig[] => JSON.parse(JSON.stringify(onChange.mock.calls.at(-1)[0]));
  return { onChange, saved };
}

const optionLabels = (select: HTMLElement) => within(select).getAllByRole('option').map((o) => o.textContent);

const LOOK_KEYS = ['style', 'style_by_size', 'density', 'density_by_size'];
const statList = (content?: Record<string, unknown>): WidgetConfig => ({
  slug: 'list',
  template: 'stat_list',
  stat_rows: [{ label: 'x', query: 'up', value_template: '{{ .Value }}' }],
  ...(content && { content }),
});
const openPerSize = () => fireEvent.click(screen.getByRole('button', { name: 'Override per size' }));

describe('WidgetBuilder looks', () => {
  test('offers every style on every template', () => {
    const templates = ['value', 'progress', 'status', 'gauge', 'stat_list', 'trend', 'countdown'];
    setup(templates.map((template) => ({ slug: template, template })));
    const selects = screen.getAllByLabelText(/^Style/);
    expect(selects).toHaveLength(templates.length);
    for (const select of selects) {
      expect(optionLabels(select)).toEqual(['Not set (choose in the app)', 'Standard', 'Bold', 'Classic']);
    }
  });

  test('defaults to not set and saves no look keys', () => {
    const { saved } = setup([{ slug: 'cpu', template: 'value', query: 'up' }, statList()]);
    for (const select of [...screen.getAllByLabelText(/^Style/), screen.getByLabelText(/^Row density/)]) {
      expect(select).toHaveValue('');
    }
    expect(screen.queryByLabelText('Small style')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /add widget/i }));
    for (const w of saved()) {
      for (const key of LOOK_KEYS) {
        expect(w.content ?? {}).not.toHaveProperty(key);
      }
    }
  });

  test('saves a picked style', () => {
    const { saved } = setup([{ slug: 'cpu', template: 'value', query: 'up' }]);
    fireEvent.change(screen.getByLabelText(/^Style/), { target: { value: 'bold' } });
    expect(saved()[0].content).toEqual({ style: 'bold' });
  });

  // Saving restarts the widget engine, whose create is a POST that replaces
  // the content, so a widget saved without the key loses the style it had.
  test('not set drops a saved style from the config', () => {
    const { saved } = setup([{ slug: 'cpu', template: 'value', query: 'up', content: { style: 'bold', unit: '%' } }]);
    expect(screen.getByLabelText(/^Style/)).toHaveValue('bold');
    fireEvent.change(screen.getByLabelText(/^Style/), { target: { value: '' } });
    expect(saved()[0].content).toEqual({ unit: '%' });
  });

  test('a template change keeps the style', () => {
    const { saved } = setup([{ slug: 'svc', template: 'status', query: 'up', content: { style: 'classic' } }]);
    fireEvent.change(screen.getByLabelText(/^Template/), { target: { value: 'gauge' } });
    expect(saved()[0].content).toEqual({ style: 'classic' });
  });

  // The server ignores case, so a hand-written "Bold" is the bold style.
  test('a capitalised style shows as its option', () => {
    setup([{ slug: 'api', template: 'status', query: 'up', content: { style: 'Bold' } }]);
    expect(screen.getByLabelText(/^Style/)).toHaveValue('bold');
  });

  test('per-size style sits collapsed and saves only the sizes set', () => {
    const { saved } = setup([{ slug: 'cpu', template: 'value', query: 'up' }]);
    expect(screen.queryByLabelText('Large style')).not.toBeInTheDocument();
    openPerSize();
    expect(screen.getAllByLabelText(/^(Small|Medium|Large|Lock Screen) style$/)).toHaveLength(4);
    const lockScreen = screen.getByLabelText('Lock Screen style');
    expect(optionLabels(lockScreen)).toEqual(['Not set', 'Standard', 'Bold', 'Classic']);

    fireEvent.change(screen.getByLabelText('Large style'), { target: { value: 'bold' } });
    expect(saved()[0].content).toEqual({ style_by_size: { large: 'bold' } });
    fireEvent.change(screen.getByLabelText('Lock Screen style'), { target: { value: 'classic' } });
    expect(saved()[0].content).toEqual({ style_by_size: { lock_screen: 'classic' } });
  });

  test('clearing the last size drops the map, clearing one keeps the rest', () => {
    const { saved } = setup([
      { slug: 'a', template: 'value', query: 'up', content: { style_by_size: { small: 'bold' } } },
      { slug: 'b', template: 'value', query: 'up', content: { style_by_size: { Small: 'Bold', LARGE: 'classic' } } },
    ]);
    // A widget with per-size looks opens with them showing, keys in any case.
    const [a, b] = screen.getAllByLabelText('Small style');
    expect(a).toHaveValue('bold');
    expect(b).toHaveValue('bold');

    fireEvent.change(a, { target: { value: '' } });
    expect(saved()[0].content).toEqual({});
    fireEvent.change(b, { target: { value: '' } });
    expect(saved()[1].content).toEqual({ style_by_size: { large: 'classic' } });
  });

  test('row density is offered on stat_list only', () => {
    setup([{ slug: 'cpu', template: 'value', query: 'up' }, statList()]);
    expect(screen.getAllByLabelText(/^Row density/)).toHaveLength(1);
    for (const button of screen.getAllByRole('button', { name: 'Override per size' })) {
      fireEvent.click(button);
    }
    expect(screen.getAllByLabelText('Small style')).toHaveLength(2);
    expect(screen.getAllByLabelText('Small row density')).toHaveLength(1);
    expect(optionLabels(screen.getByLabelText(/^Row density/))).toEqual([
      'Not set (choose in the app)',
      'Auto',
      'Compact',
      'Comfortable',
    ]);
  });

  test('saves a picked row density, for all sizes and per size', () => {
    const { saved } = setup([statList()]);
    fireEvent.change(screen.getByLabelText(/^Row density/), { target: { value: 'comfortable' } });
    expect(saved()[0].content).toEqual({ density: 'comfortable' });

    openPerSize();
    fireEvent.change(screen.getByLabelText('Lock Screen row density'), { target: { value: 'compact' } });
    expect(saved()[0].content).toEqual({ density_by_size: { lock_screen: 'compact' } });
  });

  test('moving off stat_list drops the row density and keeps the style', () => {
    const { saved } = setup([
      statList({
        style: 'bold',
        density: 'compact',
        density_by_size: { small: 'comfortable' },
        style_by_size: { large: 'classic' },
      }),
    ]);
    fireEvent.change(screen.getByLabelText(/^Template/), { target: { value: 'value' } });
    expect(saved()[0].content).toEqual({ style: 'bold', style_by_size: { large: 'classic' } });
  });
});

// A query input wider than a phone made mobile browsers zoom the whole page out
// until nothing could be read. It now fills the card, up to its desktop width.
describe('WidgetBuilder query field', () => {
  const ancestors = (el: HTMLElement) => {
    const out: HTMLElement[] = [];
    for (let e: HTMLElement | null = el; e; e = e.parentElement) {
      out.push(e);
    }
    return out;
  };

  test.each([
    ['scalar', { slug: 'cpu', template: 'gauge', query: 'up' }, 'count(up == 1)'],
    ['fan-out', { slug: 'cpu', template: 'value', query_all: 'up', slug_template: '{{.instance}}' }, undefined],
  ])('%s query has no fixed width', (_, widget, placeholder) => {
    setup([widget]);
    const input = placeholder ? screen.getByPlaceholderText(placeholder) : screen.getByDisplayValue('up');
    const chain = ancestors(input);
    expect(chain.filter((e) => getComputedStyle(e).width === '576px')).toEqual([]);
    expect(chain.some((e) => getComputedStyle(e).maxWidth === '576px')).toBe(true);
  });
});
