import { formatRfc3339, formatUnix, relativeTime } from './dates';

describe('formatUnix', () => {
  it("returns '-' for empty, zero, and NaN", () => {
    expect(formatUnix(undefined)).toBe('-');
    expect(formatUnix(0)).toBe('-');
    expect(formatUnix(NaN)).toBe('-');
  });

  it('treats values below 1e12 as seconds and the rest as milliseconds (same instant)', () => {
    expect(formatUnix(1_700_000_000)).toBe(formatUnix(1_700_000_000_000));
  });
});

describe('formatRfc3339', () => {
  it("returns '-' for empty input", () => {
    expect(formatRfc3339(undefined)).toBe('-');
    expect(formatRfc3339('')).toBe('-');
  });

  it('returns the raw string back when it cannot be parsed', () => {
    expect(formatRfc3339('not-a-date')).toBe('not-a-date');
  });

  it('formats a valid RFC3339 string rather than echoing it', () => {
    expect(formatRfc3339('2026-06-25T23:58:00Z')).not.toBe('2026-06-25T23:58:00Z');
  });
});

describe('relativeTime', () => {
  // fromNow() reads Date.now(); pin it so the relative phrase is deterministic.
  beforeAll(() => jest.useFakeTimers().setSystemTime(new Date('2026-06-26T00:00:00Z')));
  afterAll(() => jest.useRealTimers());

  it("returns '-' for empty inputs", () => {
    expect(relativeTime(undefined)).toBe('-');
    expect(relativeTime('')).toBe('-');
    expect(relativeTime(0)).toBe('-');
  });

  it('falls back to the raw string for an unparseable string and to - for NaN', () => {
    expect(relativeTime('not-a-date')).toBe('not-a-date');
    expect(relativeTime(NaN)).toBe('-');
  });

  it('renders seconds and milliseconds identically and as a relative phrase', () => {
    const seconds = Math.floor(new Date('2026-06-25T23:58:00Z').getTime() / 1000);
    expect(relativeTime(seconds)).toBe(relativeTime(seconds * 1000));
    expect(relativeTime(seconds)).toMatch(/ago/);
  });

  it('renders a valid RFC3339 string as a relative phrase (the table data shape)', () => {
    expect(relativeTime('2026-06-25T23:58:00Z')).toMatch(/ago/);
  });
});
