import { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { createActionClient } from '@fused/minion/client';
import type app from './app.js';
import type { PricePoint } from './data.js';
import './ui.css';

const actions = createActionClient<typeof app>();
type Dashboard = Awaited<ReturnType<typeof actions.getDashboard>>;
const money = (value: number) => new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(value);
const signed = (value: number) => `${value >= 0 ? '+' : '−'}${Math.abs(value).toFixed(2)}`;
const day = (value: string) => new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(new Date(value));

/** Plot only actual provider closes with an honest price axis and accessible description. */
function Chart({ points }: { points: PricePoint[] }) {
  if (points.length < 2) return <div className="chart-empty">Price history is unavailable. Try refreshing in a minute.</div>;
  const values = points.map(point => point.close);
  const bottom = Math.min(...values), top = Math.max(...values), spread = Math.max(top - bottom, 1);
  const min = bottom - spread * .15, max = top + spread * .15;
  const xy = points.map((point, i) => [16 + i / (points.length - 1) * 760, 230 - (point.close - min) / (max - min) * 210]);
  const line = xy.map(p => p.join(',')).join(' ');
  return <div className="chart-wrap"><svg viewBox="0 0 860 275" role="img" aria-label={`IBM daily closing prices from ${day(points[0]!.date)} to ${day(points.at(-1)!.date)}, between ${money(bottom)} and ${money(top)}`}>
    <defs><linearGradient id="fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor="#c3f16b" stopOpacity=".25"/><stop offset="100%" stopColor="#c3f16b" stopOpacity="0"/></linearGradient></defs>
    {[0, 1, 2, 3].map(i => <g key={i}><line x1="16" x2="780" y1={20 + i * 70} y2={20 + i * 70} className="grid-line"/><text x="796" y={25 + i * 70} className="axis">${(max - i / 3 * (max - min)).toFixed(0)}</text></g>)}
    <polygon points={`16,230 ${line} 776,230`} fill="url(#fill)"/>
    <polyline points={line} fill="none" stroke="#c3f16b" strokeWidth="3" strokeLinejoin="round"/>
    <circle cx={xy.at(-1)![0]} cy={xy.at(-1)![1]} r="5" fill="#c3f16b"/>
    <text x="16" y="267" className="axis">{day(points[0]!.date)}</text><text x="776" y="267" textAnchor="end" className="axis">{day(points.at(-1)!.date)}</text>
  </svg></div>;
}

/** Keep previous data visibly dated during refresh failures, without synthesizing market values. */
function StocksApp() {
  const [data, setData] = useState<Dashboard>();
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState('');
  const [range, setRange] = useState<22 | 66>(22);
  const inFlight = useRef(false);
  async function refresh() {
    if (inFlight.current) return;
    inFlight.current = true; setBusy(true); setError('');
    try { setData(await actions.getDashboard({ symbol: 'IBM' })); }
    catch { setError('Could not load prices from Fused. Please try again in a minute.'); }
    finally { inFlight.current = false; setBusy(false); }
  }
  useEffect(() => { void refresh(); }, []);
  const quote = data?.quote;
  return <div className="app-shell">
    <header><a className="brand" href="/"><span className="brand-icon">m</span>minion<span className="brand-divider">/</span><span className="brand-product">stocks</span></a><span className="environment"><span className="status-dot"/>Linux VM</span></header>
    <main><div className="page-title"><div><p className="eyebrow">YOUR MARKET, AT A GLANCE</p><h1>A little market perspective.</h1><p className="subtitle">Stock prices. A clear view. Powered by Fused.</p></div><button className="refresh" onClick={() => void refresh()} disabled={busy}><span aria-hidden="true">↻</span>{busy ? 'Refreshing…' : 'Refresh prices'}</button></div>
      <div className="demo-note"><span className="demo-tag">DEMO FEED</span><p>IBM via Alpha Vantage’s public demo. End-of-day data, not real-time quotes.</p><span className="currency">USD</span></div>
      {error && <div role="alert" className="error">{error}{data && ' Showing the last successful result below.'}</div>}
      <div className="dashboard-grid"><section className="price-panel" aria-labelledby="stock-title">
        <div className="stock-heading"><div className="company-icon">IBM</div><div><h2 id="stock-title">International Business Machines</h2><p>IBM <span>·</span> NYSE</p></div><span className="stock-kind">EQUITY</span></div>
        <div className="price-row"><div><div className="big-price">{quote ? money(quote.price) : '—'}<span>USD</span></div><div className={quote && quote.change < 0 ? 'change negative' : 'change'}>{quote ? `${signed(quote.change)} (${signed(quote.changePercent)}%)` : 'Waiting for market data'}<span>{quote ? 'vs. previous close' : ''}</span></div></div><div className="quote-date"><span>Latest trading day</span><strong>{quote ? day(quote.tradingDay) : '—'}</strong></div></div>
        <div className="chart-heading"><h3>Price history <span>Daily close</span></h3><div className="range-picker" aria-label="Chart range"><button aria-pressed={range === 22} onClick={() => setRange(22)}>1M</button><button aria-pressed={range === 66} onClick={() => setRange(66)}>3M</button></div></div>
        {data ? <Chart points={data.history.slice(-range)}/> : <div className="chart-empty" role="status">{busy ? 'Loading daily closing prices…' : 'Market data is unavailable.'}</div>}
        <div className="chart-footer"><span><i/>Daily closing price</span><span>{data ? `${data.history.slice(-range).length} trading sessions` : 'No data loaded'}</span></div>
      </section><aside>
        <section className="watchlist"><div className="section-heading"><h2>Watchlist</h2><span>01</span></div><div className="watch-item"><div className="watch-symbol">IBM</div><div><strong>IBM</strong><p>International Business Machines</p></div><div className="watch-price"><strong>{quote ? money(quote.price) : '—'}</strong><span className={quote && quote.change < 0 ? 'negative' : ''}>{quote ? `${signed(quote.changePercent)}%` : '—'}</span></div></div><div className="watch-note">This demo is scoped to IBM. Connect your own market-data key in Fused to support more symbols.</div></section>
        <section className="connection"><div className="connection-icon">✳</div><h2>Connected through Fused</h2><p>Your Minion runs independently.<br/>Fused handles the integration.</p><div className="connection-row"><span>App runtime</span><strong>Hostlier / Linux</strong></div><div className="connection-row"><span>Engine transport</span><strong>gRPC + TLS</strong></div><div className="connection-row"><span>Market data</span><strong>Alpha Vantage</strong></div></section>
      </aside></div>
      <section className="stats" aria-label="Trading session statistics">{[['Open', quote?.open], ['Day high', quote?.high], ['Day low', quote?.low], ['Previous close', quote?.previousClose]].map(([label, value]) => <div key={label as string}><span>{label}</span><strong>{typeof value === 'number' ? money(value) : '—'}</strong></div>)}<div><span>Volume</span><strong>{quote ? new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 2 }).format(quote.volume) : '—'}</strong></div></section>
      <footer><span>MINION <span className="footer-dot">·</span> A Fused mini app</span><span role="status">{busy ? 'Fetching through Fused…' : data ? `Fetched ${new Date(data.fetchedAt).toLocaleTimeString()} · cached for 60s` : 'No market data loaded'}</span><a href="https://www.alphavantage.co/documentation/" target="_blank" rel="noreferrer">Data source ↗</a></footer>
    </main>
  </div>;
}
const root = document.getElementById('app');
if (!root) throw new Error('Minion mount point missing');
createRoot(root).render(<StocksApp/>);
