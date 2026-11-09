import * as z from 'zod/mini';

export const pointSchema = z.object({ date: z.string(), close: z.number() });
export const quoteSchema = z.object({ symbol: z.literal('IBM'), price: z.number(), open: z.number(), high: z.number(), low: z.number(), previousClose: z.number(), change: z.number(), changePercent: z.number(), volume: z.number(), tradingDay: z.string() });
export type PricePoint = z.infer<typeof pointSchema>;
export type Quote = z.infer<typeof quoteSchema>;
const text = z.string().check(z.minLength(1));
const rawQuote = z.object({ 'Global Quote': z.object({ '01. symbol': z.literal('IBM'), '02. open': text, '03. high': text, '04. low': text, '05. price': text, '06. volume': text, '07. latest trading day': text, '08. previous close': text, '09. change': text, '10. change percent': text }) });
const rawHistory = z.object({ 'Meta Data': z.object({ '2. Symbol': z.literal('IBM') }), 'Time Series (Daily)': z.record(z.string(), z.object({ '4. close': text })) });

/** Fail closed on provider notices, missing fields, and nonnumeric price strings. */
function number(value: string): number {
  if (!/^-?\d+(?:\.\d+)?%?$/.test(value)) throw new Error('Invalid market value');
  const parsed = Number(value.replace(/%$/, ''));
  if (!Number.isFinite(parsed)) throw new Error('Invalid market value');
  return parsed;
}

/** Preserve the provider trading date rather than presenting retrieval time as quote time. */
function date(value: string): string {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value) || new Date(value).toISOString().slice(0, 10) !== value) throw new Error('Invalid trading date');
  return value;
}

/** Normalize documented Alpha Vantage fields into the public app contract. */
export function parseQuote(value: unknown): Quote {
  const q = z.parse(rawQuote, value)['Global Quote'];
  const result = { symbol: q['01. symbol'], price: number(q['05. price']), open: number(q['02. open']), high: number(q['03. high']), low: number(q['04. low']), previousClose: number(q['08. previous close']), change: number(q['09. change']), changePercent: number(q['10. change percent']), volume: number(q['06. volume']), tradingDay: date(q['07. latest trading day']) };
  if (result.price <= 0 || result.low <= 0 || result.high < result.low || result.volume < 0) throw new Error('Invalid quote');
  return z.parse(quoteSchema, result);
}

/** Sort valid daily closes chronologically; never invent points for missing trading days. */
export function parseHistory(value: unknown): PricePoint[] {
  const series = z.parse(rawHistory, value)['Time Series (Daily)'];
  const points = Object.entries(series).map(([day, q]) => ({ date: date(day), close: number(q['4. close']) })).sort((a, b) => a.date.localeCompare(b.date));
  if (!points.length || points.some(point => point.close <= 0)) throw new Error('Invalid price history');
  return points.slice(-100);
}

/** Derive the current end-of-day quote from two actual trading sessions, never retrieval time. */
export function parseDailyQuote(value: unknown): Quote {
  const history = parseHistory(value);
  if (history.length < 2) throw new Error('Two trading sessions are required');
  const latest = history.at(-1)!;
  const previous = history.at(-2)!;
  const series = z.parse(z.object({ 'Time Series (Daily)': z.record(z.string(), z.object({ '1. open': text, '2. high': text, '3. low': text, '4. close': text, '5. volume': text })) }), value)['Time Series (Daily)'];
  const row = series[latest.date]!;
  const change = latest.close - previous.close;
  return parseQuote({ 'Global Quote': { '01. symbol': 'IBM', '02. open': row['1. open'], '03. high': row['2. high'], '04. low': row['3. low'], '05. price': row['4. close'], '06. volume': row['5. volume'], '07. latest trading day': latest.date, '08. previous close': String(previous.close), '09. change': change.toFixed(6), '10. change percent': (change / previous.close * 100).toFixed(6) } });
}
