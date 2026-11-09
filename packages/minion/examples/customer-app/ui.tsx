import { useState, type FormEvent } from 'react';
import { createRoot } from 'react-dom/client';
import { createActionClient } from '@fused/minion/client';
import type app from './app.js';
import './ui.css';

/** Render the optional UI with ordinary JSX and request-local form state. */
function CustomerApp() {
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState('');

  /** Keep the app access token in memory and call only the typed server action. */
  async function createCustomer(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setStatus('Creating customer…');
    const actions = createActionClient<typeof app>({ headers: () => ({ Authorization: `Bearer ${token}` }) });
    try {
      const result = await actions.createCustomer({ name });
      setStatus(`Created customer ${result.customerId}`);
    } catch {
      setStatus('Could not create customer. Check your app access and connection.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <main>
      <h1>Customers</h1>
      <p>Create a customer through your connected Fused service.</p>
      <form onSubmit={createCustomer}>
        <label>
          App access token
          <input name="token" type="password" required autoComplete="off" value={token}
            onChange={event => setToken(event.currentTarget.value)} disabled={busy} />
        </label>
        <label>
          Customer name
          <input name="name" required maxLength={100} value={name}
            onChange={event => setName(event.currentTarget.value)} disabled={busy} />
        </label>
        <button disabled={busy}>{busy ? 'Creating…' : 'Create customer'}</button>
      </form>
      <p role="status" aria-live="polite">{status}</p>
    </main>
  );
}

const root = document.getElementById('app');
// The generated HTML shell owns this mount point for every optional UI.
if (!root) throw new Error('Minion UI mount point is missing');
createRoot(root).render(<CustomerApp />);
