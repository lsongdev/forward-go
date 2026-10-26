import { html, useState } from '../lib/html.js';
import { useAction, useResource } from '../lib/hooks.js';
import { Page, Notice, Loading, Empty, Status, Field, Modal, confirmDanger, Table } from '../lib/ui.js';
import { usePanel } from '../lib/panel.js';
import { bytes } from '../lib/format.js';

const PROTOCOLS = { tcp: 'TCP', udp: 'UDP' };

function State({ rule }) {
  const s = rule.stats;
  if (s.running) return html`<${Status} state="running" />`;
  if (rule.enabled) return html`<${Status} state="failed" label="Not running" title=${s.error} />`;
  return html`<${Status} state="stopped" label="Disabled" />`;
}

export default function Rules() {
  const { api } = usePanel();
  const rules = useResource(signal => api.get('/rules', { signal }), [], { interval: 3000 });
  const action = useAction();
  const [editing, setEditing] = useState(null); // a rule, or {} for a new one

  const toggle = r => action.run(async () => {
    await api.post(`/rules/${r.id}/${r.enabled ? 'disable' : 'enable'}`);
    rules.reload();
  });
  const remove = r => {
    if (!confirmDanger(`Delete the rule ${r.name}? Its open connections are closed.`)) return;
    action.run(async () => { await api.del(`/rules/${r.id}`); rules.reload(); });
  };

  return html`<${Page} title="Port forwards" subtitle="Forward TCP connections or UDP datagrams on a local port to another address."
    actions=${html`<button class="button button-primary" onClick=${() => setEditing({})}>Add rule</button>`}>
    <${Notice} error=${rules.error || action.error} />
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
          <td class="actions"><span class="button-row">
            <button class="button button-small" disabled=${action.busy} onClick=${() => toggle(r)}>${r.enabled ? 'Disable' : 'Enable'}</button>
            <button class="button button-small" onClick=${() => setEditing(r)}>Edit</button>
            <button class="button button-small button-danger" disabled=${action.busy} onClick=${() => remove(r)}>Delete</button>
          </span></td>
        </tr>`)}</tbody>
      <//>`}
    ${editing ? html`<${RuleForm} rule=${editing} onClose=${() => setEditing(null)} onSaved=${() => { setEditing(null); rules.reload(); }} />` : null}
  <//>`;
}

function RuleForm({ rule, onClose, onSaved }) {
  const { api } = usePanel();
  const isNew = !rule.id;
  const [r, setR] = useState({
    name: rule.name || '', protocol: rule.protocol || 'tcp', listen: rule.listen || '',
    target: rule.target || '', enabled: rule.enabled ?? true,
  });
  const { busy, error, run } = useAction();
  const set = patch => setR(prev => ({ ...prev, ...patch }));
  const submit = e => {
    e.preventDefault();
    run(async () => {
      if (isNew) await api.post('/rules', r);
      else await api.put(`/rules/${rule.id}`, r);
      onSaved();
    });
  };
  return html`<${Modal} title=${isNew ? 'Add rule' : `Edit ${rule.name}`} onClose=${onClose} footer=${html`
    <span class="spacer"></span>
    <button class="button" type="button" onClick=${onClose}>Cancel</button>
    <button class="button button-primary" type="submit" form="rule-form" disabled=${busy}>${isNew ? 'Add' : 'Save'}</button>`}>
    <form id="rule-form" class="form" onSubmit=${submit}>
      <${Notice} error=${error} />
      <${Field} label="Name" required>
        <input class="input" required value=${r.name} onInput=${e => set({ name: e.currentTarget.value })} placeholder="nas-ssh" autofocus />
      <//>
      <fieldset class="field"><span class="field-label">Protocols</span><div class="choices">
        ${Object.entries(PROTOCOLS).map(([k, label]) => html`<label class="field-check" key=${k}>
          <input type="radio" name="protocol" checked=${r.protocol === k} onChange=${() => set({ protocol: k })} />${label}</label>`)}
      </div></fieldset>
      <div class="form-grid">
        <${Field} label="Listen on" required hint="A port, or address:port; a port alone listens on every interface">
          <input class="input mono" required value=${r.listen} onInput=${e => set({ listen: e.currentTarget.value })} placeholder="2222" />
        <//>
        <${Field} label="Target" required hint="host:port">
          <input class="input mono" required value=${r.target} onInput=${e => set({ target: e.currentTarget.value })} placeholder="192.168.1.10:22" />
        <//>
      </div>
      <label class="field-check"><input type="checkbox" checked=${r.enabled} onChange=${e => set({ enabled: e.currentTarget.checked })} />Enable on save</label>
    </form>
  <//>`;
}
