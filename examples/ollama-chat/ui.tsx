import { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { createActionClient } from '@fused/minion/client';
import type app from './app.js';
import './ui.css';

type Tool = { name: string; arguments: string; result: string };
type Message = { id: string; role: 'user' | 'assistant'; text: string; tools?: Tool[]; elapsedMs?: number; totalTokens?: number };
type Conversation = { id: string; title: string; sessionId?: string; messages: Message[] };
const suggestions = [
  { icon: 'calculate', title: 'Put it to work', subtitle: 'Try a calculation with tools', prompt: 'Use calculate to multiply 17 by 23, then explain the result.' },
  { icon: 'clock', title: 'Get the current time', subtitle: 'Ask for a live answer', prompt: 'Use utc_time to tell me the current UTC time.' },
  { icon: 'spark', title: 'Explore an idea', subtitle: 'A little help thinking it through', prompt: 'Give me three creative ideas for a small weekend coding project.' },
];
const errors: Record<string, string> = {
  BUSY: 'The agent is finishing another request. Give it a moment, then try again.',
  SESSION_EXPIRED: 'This conversation is no longer available on the agent. Start a new chat to continue.',
  AGENT_UNAVAILABLE: 'The agent could not answer right now. Your message is saved below; you can try again.',
  CONNECTION_FAILED: 'The agent took too long or the connection was interrupted. You can try again.',
  INPUT_TOO_LARGE: 'Please shorten your message and try again.',
  INVALID_RESPONSE: 'The agent did not return a complete answer. You can try again.',
};
function Icon({ name, size = 20 }: { name: string; size?: number }) {
  const paths: Record<string, string> = {
    plus: 'M12 5v14M5 12h14', arrow: 'M12 19V5m-6 6 6-6 6 6', chat: 'M21 11.5a8.5 8.5 0 0 1-8.5 8.5H4l-2 2V11.5a9.5 9.5 0 0 1 19 0Z',
    clock: 'M12 8v4l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z',
    calculate: 'M8 3h8a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Zm1 4h6M9 12h1m4 0h1m-6 4h1m4 0h1',
    spark: 'm12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5L12 3Z',
    lock: 'M7 10V7a5 5 0 0 1 10 0v3M5 10h14v11H5V10Zm7 5v2',
    copy: 'M9 8h11v13H9V8ZM5 16H3V3h11v2',
    check: 'm5 12 4 4L19 6', exit: 'M9 4H4v16h5m5-12 4 4-4 4m-6-4h10',
  };
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={paths[name] ?? paths.spark} /></svg>;
}
function Mark({ small = false }: { small?: boolean }) { return <span className={`mark ${small ? 'small' : ''}`} aria-hidden="true"><span /><span /></span>; }
function Text({ value }: { value: string }) {
  return <>{value.split(/(\*\*[^*\n]+\*\*|`[^`\n]+`)/g).map((part, index) => part.startsWith('**') && part.endsWith('**') ? <strong key={index}>{part.slice(2, -2)}</strong> : part.startsWith('`') && part.endsWith('`') ? <code key={index}>{part.slice(1, -1)}</code> : part)}</>;
}
function MessageView({ message }: { message: Message }) {
  const [copied, setCopied] = useState(false);
  const assistant = message.role === 'assistant';
  return <article className={`message ${message.role}`}>
    <div className="message-avatar">{assistant ? <Mark small /> : 'Y'}</div>
    <div className="message-content">
      <div className="message-author">{assistant ? 'Ollama assistant' : 'You'}{assistant && <span>Harnest</span>}</div>
      {!!message.tools?.length && <div className="tools">{message.tools.map((tool, index) => <details key={index}><summary><Icon name={tool.name === 'utc_time' ? 'clock' : 'calculate'} size={15} /><span>{tool.name === 'utc_time' ? 'Checked the time' : 'Used calculator'}</span><span className="tool-check">✓</span></summary><div className="tool-body"><b>Arguments</b><pre>{tool.arguments}</pre><b>Result</b><pre>{tool.result}</pre></div></details>)}</div>}
      <div className="message-text"><Text value={message.text} /></div>
      {assistant && <div className="message-meta"><span>{((message.elapsedMs ?? 0) / 1000).toFixed(1)}s{message.totalTokens !== undefined ? ` · ${message.totalTokens.toLocaleString()} tokens` : ''}</span><button className="icon-button" aria-label="Copy answer" onClick={async () => { try { await navigator.clipboard.writeText(message.text); setCopied(true); setTimeout(() => setCopied(false), 1800); } catch { setCopied(false); } }}><Icon name={copied ? 'check' : 'copy'} size={15} />{copied ? 'Copied' : 'Copy'}</button></div>}
    </div>
  </article>;
}
function App() {
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [activeId, setActiveId] = useState<string>();
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [elapsed, setElapsed] = useState(0);
  const abort = useRef<AbortController | null>(null);
  const end = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const active = conversations.find(item => item.id === activeId);
  const actions = createActionClient<typeof app>();
  useEffect(() => { end.current?.scrollIntoView({ behavior: 'smooth' }); }, [active?.messages.length, busy, error]);
  useEffect(() => {
    if (!busy) { setElapsed(0); return; }
    const start = Date.now();
    const timer = setInterval(() => setElapsed(Math.floor((Date.now() - start) / 1000)), 1000);
    return () => clearInterval(timer);
  }, [busy]);
  useEffect(() => () => abort.current?.abort(), []);

  function newChat() { if (busy) return; setActiveId(undefined); setDraft(''); setError(''); input.current?.focus(); }
  async function send(prompt = draft) {
    prompt = prompt.trim();
    if (!prompt || busy) return;
    const conversation = active ?? { id: crypto.randomUUID(), title: prompt.slice(0, 44), messages: [] };
    const user: Message = { id: crypto.randomUUID(), role: 'user', text: prompt };
    setConversations(items => active ? items.map(item => item.id === conversation.id ? { ...item, messages: [...item.messages, user] } : item) : [...items, { ...conversation, messages: [user] }]);
    setActiveId(conversation.id); setDraft(''); setError(''); setBusy(true);
    const controller = new AbortController(); abort.current = controller;
    try {
      const reply = await actions.chat({ prompt, sessionId: conversation.sessionId, history: conversation.messages.slice(-6).map(message => ({ role: message.role, text: message.text.slice(0, 2000) })) }, { signal: controller.signal });
      if (!reply.ok) { setError(errors[reply.error ?? ''] ?? 'The request could not be completed. Please try again.'); setDraft(prompt); return; }
      setConversations(items => items.map(item => item.id === conversation.id ? { ...item, sessionId: reply.sessionId, messages: [...item.messages, { id: crypto.randomUUID(), role: 'assistant', text: reply.text, tools: reply.tools, elapsedMs: reply.elapsedMs, totalTokens: reply.totalTokens }] } : item));
    } catch (cause) {
      if (controller.signal.aborted) { setError('Stopped waiting for this response. The agent may still finish its current work.'); setDraft(prompt); }
      else { setError('Connection interrupted. Your message is available below to try again.'); setDraft(prompt); }
    } finally { setBusy(false); abort.current = null; }
  }
  return <div className="shell">
    <aside className="sidebar">
      <a className="brand" href="/" aria-label="Orbit home"><Mark small /><span>orbit<span className="brand-dot">.</span></span></a>
      <div className="workspace"><span className="workspace-avatar">O</span><div><strong>Your workspace</strong><span>Personal playground</span></div><span className="workspace-chevron">⌄</span></div>
      <button className="new-chat" onClick={newChat} disabled={busy}><Icon name="plus" size={18} />New conversation<span>↗</span></button>
      <div className="nav-label">CONVERSATIONS <span>{conversations.length.toString().padStart(2, '0')}</span></div>
      <nav aria-label="Conversations">{conversations.length ? [...conversations].reverse().map(item => <button key={item.id} className={`conversation-link ${activeId === item.id ? 'selected' : ''}`} disabled={busy} onClick={() => { setActiveId(item.id); setError(''); setDraft(''); }}><Icon name="chat" size={16} /><span>{item.title}</span></button>) : <div className="empty-history">A fresh start.<br />Your conversations will appear here.</div>}</nav>
      <div className="sidebar-bottom"><div className="agent-card"><span className="online-dot" /><div><strong>Ollama assistant</strong><span>Powered by Harnest</span></div><span className="agent-glyph">↗</span></div><div className="built-with">Built with <b>Minion</b><span>Hosted by Hostlier</span></div></div>
    </aside>
    <main className="main">
      <header className="topbar"><div className="breadcrumb">Playground<span>/</span><strong>{active ? 'Conversation' : 'New conversation'}</strong></div><div className="header-right"><span className="connection"><i />Public playground</span></div></header>
      <div className="conversation-area">
        {!active?.messages.length ? <section className="welcome"><span className="eyebrow"><span /> YOUR AGENT, READY WHEN YOU ARE</span><Mark /><h1>A little curiosity.<br /><span>A lot of possibility.</span></h1><p>Meet your Ollama assistant. Ask a question, put its tools<br className="desktop-break" /> to work, or follow an idea somewhere new.</p><div className="suggestions">{suggestions.map(item => <button key={item.title} onClick={() => { setDraft(item.prompt); input.current?.focus(); }}><Icon name={item.icon} size={22} /><strong>{item.title}</strong><span>{item.subtitle}</span><span className="card-arrow">↗</span></button>)}</div><div className="model-note"><span className="model-icon">✳</span>Ollama Cloud<span className="separator">·</span>Tools enabled</div></section> : <section className="messages" aria-label="Conversation messages">{active.messages.map(message => <MessageView key={message.id} message={message} />)}{busy && <div className="thinking" role="status"><Mark small /><div><strong>Working on it<span className="thinking-dots">…</span></strong><span>The agent is thinking and checking its tools · {elapsed}s</span></div></div>}<div ref={end} /></section>}
      </div>
      <div className="composer-area">{error && <div className="error-banner" role="alert">{error}<button aria-label="Dismiss error" onClick={() => setError('')}>×</button></div>}<form className={`composer ${busy ? 'is-busy' : ''}`} onSubmit={event => { event.preventDefault(); void send(); }}><label className="sr-only" htmlFor="prompt">Message the agent</label><textarea id="prompt" ref={input} value={draft} maxLength={6000} rows={2} placeholder="Ask your agent anything…" onChange={event => setDraft(event.target.value)} onKeyDown={event => { if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); void send(); } }} disabled={busy} /><div className="composer-footer"><span><Icon name="spark" size={15} />Ollama assistant<span className="composer-divider" />{draft.length > 5500 ? `${draft.length}/6000` : 'Calculator + UTC time'}</span>{busy ? <button type="button" className="send stop" onClick={() => abort.current?.abort()} aria-label="Stop response"><span /></button> : <button className="send" type="submit" disabled={!draft.trim()} aria-label="Send message"><Icon name="arrow" size={21} /></button>}</div></form><div className="composer-hint"><span>Enter to send <span className="hint-dot">·</span> Shift + Enter for a new line</span><span>Answers can be imperfect. Stay curious.</span></div></div>
    </main>
  </div>;
}
const root = document.getElementById('app');
if (!root) throw new Error('Missing mount point');
createRoot(root).render(<App />);
