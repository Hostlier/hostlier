import { createElement, type ComponentType } from 'react';
import { renderToString } from 'react-dom/server';
import type { ActionClient } from './client.js';
import type { Actions } from './index.js';

/** Only loader data crosses the HTML boundary; the action client is recreated in the browser. */
export function pageRenderer(shell: string, Page: ComponentType<any>, load: (actions: ActionClient<Actions>) => Promise<unknown>) {
  return async (actions: ActionClient<Actions>) => {
    const json = JSON.stringify(await load(actions));
    if (!json || Buffer.byteLength(json) > 1024 * 1024) throw new Error('Page data must fit within 1 MiB');
    const data = JSON.parse(json);
    const markup = renderToString(createElement(Page, { data, actions }));
    // Inert JSON cannot close its script element, even when it contains provider-supplied HTML.
    const state = json.replace(/</g, '\\u003c');
    return shell.replace('<div id="app"></div>', () => `<div id="app">${markup}</div><script type="application/json" id="minion-data">${state}</script>`);
  };
}
