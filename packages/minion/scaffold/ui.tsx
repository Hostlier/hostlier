import { useState, type FormEvent } from 'react';
import type { PageProps } from '@fused/minion/client';
import type load from './load.js';

export default function Greeting({ data, actions }: PageProps<typeof load>) {
  const [name, setName] = useState('');
  const [message, setMessage] = useState(data.greeting);
  const [busy, setBusy] = useState(false);

  async function greet(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    try {
      setMessage(await actions.greet({ name: name.trim() }));
    } catch {
      setMessage('Could not load the greeting. Try again.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <main>
      <h1>Hello Minion</h1>
      <form onSubmit={greet}>
        <label>Your name <input value={name} onChange={e => setName(e.target.value)} /></label>
        <button type="submit" disabled={busy || !name.trim()}>Say hello</button>
      </form>
      <p role="status">{message}</p>
    </main>
  );
}
