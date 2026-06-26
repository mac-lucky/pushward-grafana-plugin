import React from 'react';
import { render, screen } from '@testing-library/react';
import { RelativeTimeCell } from './RelativeTimeCell';

describe('RelativeTimeCell', () => {
  it('renders a bare dash for empty input', () => {
    render(<RelativeTimeCell value={undefined} />);
    expect(screen.getByText('-')).toBeInTheDocument();
  });

  it('renders a relative phrase for a timestamp', () => {
    jest.useFakeTimers().setSystemTime(new Date('2026-06-26T00:00:00Z'));
    const seconds = Math.floor(new Date('2026-06-25T23:55:00Z').getTime() / 1000);
    render(<RelativeTimeCell value={seconds} />);
    expect(screen.getByText(/ago/)).toBeInTheDocument();
    jest.useRealTimers();
  });
});
