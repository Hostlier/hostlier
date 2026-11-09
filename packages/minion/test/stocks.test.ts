import test from 'node:test';
import assert from 'node:assert/strict';
import { parseQuote, parseHistory, parseDailyQuote } from '../examples/stocks-app/data.js';

const quote = { 'Global Quote': { '01. symbol': 'IBM', '02. open': '249.2700', '03. high': '258.3300', '04. low': '247.8001', '05. price': '258.2800', '06. volume': '3934452', '07. latest trading day': '2026-10-29', '08. previous close': '253.2200', '09. change': '5.0600', '10. change percent': '1.9983%' } };

test('stock quote preserves provider trading date and numeric values', () => {
  const result = parseQuote(quote);
  assert.equal(result.price, 258.28);
  assert.equal(result.tradingDay, '2026-10-29');
  assert.equal(result.changePercent, 1.9983);
  assert.equal(result.volume, 3934452);
});

test('stock parsing rejects provider notices, wrong symbols, empty values and invalid dates', () => {
  for (const value of [{ Information: 'API limit' }, { 'Global Quote': {} }, { 'Global Quote': { ...quote['Global Quote'], '01. symbol': 'AAPL' } }, { 'Global Quote': { ...quote['Global Quote'], '05. price': '' } }, { 'Global Quote': { ...quote['Global Quote'], '07. latest trading day': '2026-02-30' } }]) assert.throws(() => parseQuote(value));
});

test('stock history plots real trading dates in chronological order and rejects missing prices', () => {
  const value = { 'Meta Data': { '2. Symbol': 'IBM' }, 'Time Series (Daily)': { '2026-10-29': { '4. close': '258.28' }, '2026-10-27': { '4. close': '250.22' } } };
  assert.deepEqual(parseHistory(value), [{ date: '2026-10-27', close: 250.22 }, { date: '2026-10-29', close: 258.28 }]);
  assert.throws(() => parseHistory({ Information: 'Rate limit' }));
  assert.throws(() => parseHistory({ ...value, 'Time Series (Daily)': { '2026-10-29': { '4. close': 'NaN' } } }));
});

test('daily quote uses the latest two trading sessions and rejects incomplete daily payloads', () => {
  const row = { '1. open': '250', '2. high': '260', '3. low': '249', '4. close': '258.28', '5. volume': '3934452' };
  const series = { 'Meta Data': { '2. Symbol': 'IBM' }, 'Time Series (Daily)': { '2026-10-29': row, '2026-10-28': { ...row, '4. close': '253.22' } } };
  const result = parseDailyQuote(series);
  assert.equal(result.price, 258.28);
  assert.equal(result.previousClose, 253.22);
  assert.equal(result.change, 5.06);
  assert.equal(result.tradingDay, '2026-10-29');
  assert.throws(() => parseDailyQuote({ Information: 'API limit' }));
  assert.throws(() => parseDailyQuote({ ...series, 'Time Series (Daily)': { '2026-10-29': row } }));
  assert.throws(() => parseDailyQuote({ ...series, 'Time Series (Daily)': { ...series['Time Series (Daily)'], '2026-10-29': { ...row, '5. volume': '' } } }));
});
