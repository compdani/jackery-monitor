export function fmtMoney(n: number | null | undefined, currency = 'USD'): string {
  if (n == null || !Number.isFinite(n)) return '—';
  try {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: currency || 'USD',
      maximumFractionDigits: 2,
    }).format(n);
  } catch {
    return `$${Number(n).toFixed(2)}`;
  }
}

export type Savings = {
  solar_savings?: number;
  grid_cost?: number;
  net_savings?: number;
};

export function savingsLines(savings: Savings | null | undefined, currency = 'USD') {
  if (!savings) return null;
  const net = savings.net_savings ?? 0;
  return {
    solar: `+${fmtMoney(savings.solar_savings, currency)} solar`,
    grid: `−${fmtMoney(savings.grid_cost, currency)} grid`,
    net: `${net >= 0 ? '+' : ''}${fmtMoney(net, currency)} net`,
  };
}
