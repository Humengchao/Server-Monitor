import { useEffect, useState } from 'react';

const EXCHANGE_RATES_URL = 'https://api.frankfurter.dev/v2/rates?base=EUR&quotes=CNY,USD';

// Approximate rates keep cost totals readable when the daily rate service is
// unreachable (offline deployments, blocked egress). They are only a fallback:
// a successful fetch always wins.
export const FALLBACK_RATES_PER_EUR: Record<string, number> = { EUR: 1, CNY: 7.8, USD: 1.1 };

interface ExchangeRateResponse {
  date: string;
  base: string;
  quote: string;
  rate: number;
}

export const currencySymbol = (currency: string) =>
  ({ CNY: '¥', USD: '$', EUR: '€' }[currency] || currency || '¥');

export function convertCurrency(
  amount: number,
  from: string,
  to: string,
  ratesPerEUR: Record<string, number>,
): number {
  const sourceRate = ratesPerEUR[from] || ratesPerEUR.CNY;
  const targetRate = ratesPerEUR[to] || ratesPerEUR.CNY;
  return (amount / sourceRate) * targetRate;
}

/**
 * Fetches EUR-based conversion rates once per mount. Shared by the public
 * status page and the dashboard cost summary so both show the same numbers.
 */
export function useExchangeRates(): Record<string, number> {
  const [ratesPerEUR, setRatesPerEUR] = useState<Record<string, number>>(FALLBACK_RATES_PER_EUR);

  useEffect(() => {
    const controller = new AbortController();
    (async () => {
      try {
        const response = await fetch(EXCHANGE_RATES_URL, {
          headers: { Accept: 'application/json' },
          signal: controller.signal,
        });
        if (!response.ok) throw new Error('exchange rate request failed');
        const rates = (await response.json()) as ExchangeRateResponse[];
        const next = { ...FALLBACK_RATES_PER_EUR };
        for (const item of rates) {
          if (item.base === 'EUR' && Number.isFinite(item.rate) && item.rate > 0) {
            next[item.quote] = item.rate;
          }
        }
        setRatesPerEUR(next);
      } catch {
        // Keep the fallback rates.
      }
    })();
    return () => controller.abort();
  }, []);

  return ratesPerEUR;
}
