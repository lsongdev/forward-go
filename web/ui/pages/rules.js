import { html } from '@lsongdev/panel/html.js';
import { useAction, useResource } from '@lsongdev/panel/hooks.js';
import { PageHeader, Alert, Loading, Empty, Table } from '@lsongdev/scripts/react/components/index.js';
import { Status } from '@lsongdev/panel/ui.js';
import { usePanel } from '@lsongdev/panel/panel.js';
import { bytes } from '@lsongdev/panel/format.js';

const PROTOCOLS = { tcp: 'TCP', udp: 'UDP' };

function State({ rule }) {
  const s = rule.stats;
  if (s.running) return html`<${Status} state="running" />`;
  if (rule.enabled) return html`<${Status} state="failed" label="Not running" title=${s.error} />`;
  return html`<${Status} state="stopped" label="Disabled" />`;
}

export default function Rules() {
  const { api, href } = usePanel();
  const rules = useResource(signal => api.get('/rules', { signal }), [], { interval: 3000 });
  const action = useAction();

  const toggle = r => action.run(async () => {
    await api.post(`/rules/${r.id}/${r.enabled ? 'disable' : 'enable'}`);
    rules.reload();
  });
  const remove = r => {
    if (!window.confirm(`Delete the rule ${r.name}? Its open connections are closed.`)) return;
    action.run(async () => { await api.del(`/rules/${r.id}`); rules.reload(); });
  };

  return html`<${PageHeader} title="Port forwards" subtitle="Forward TCP connections or UDP datagrams on a local port to another address."
    actions=${html`<a class="button button-primary" href=${href('/rules/new')}>Add rule</a>`} />
    <${Alert} error=${rules.error || action.error} />
    ${rules.data === undefined ? (rules.error ? null : html`<${Loading} />`) : !rules.data.length ? html`<${Empty}>No rules yet.<//>` : html`
      <${Table}>
        <thead><tr><th>Name</th><th>Forward</th><th>Status</th><th class="num">Connections</th><th class="num">Traffic in / out</th><th class="actions"></th></tr></thead>
        <tbody>${rules.data.map(r => html`<tr key=${r.id}>
          <td><div class="cell-main">${r.name}</div><div class="cell-sub">${PROTOCOLS[r.protocol]}</div></td>
          <td><span class="route mono"><span>${r.listen}</span><span class="arrow">→</span><span>${r.target}</span></span>
            ${r.enabled && r.stats.error ? html`<div class="error-text">${r.stats.error}</div>` : null}</td>
          <td><${State} rule=${r} /></td>
          <td class="num">${r.stats.running ? html`${r.stats.active}<div class="cell-sub">${r.stats.total} total</div>` : '—'}</td>
          <td class="num nowrap">${r.stats.running ? `${bytes(r.stats.bytes_in)} / ${bytes(r.stats.bytes_out)}` : '—'}</td>
          <td class="actions"><span class="row-actions">
            <button class="button button-small" disabled=${action.busy} onClick=${() => toggle(r)}>${r.enabled ? 'Disable' : 'Enable'}</button>
            <a class="button button-small" href=${href(`/rules/${r.id}/edit`)}>Edit</a>
            <button class="button button-small button-danger" disabled=${action.busy} onClick=${() => remove(r)}>Delete</button>
          </span></td>
        </tr>`)}</tbody>
      <//>`}`;
}
