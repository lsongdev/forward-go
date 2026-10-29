import { html, useState } from '../lib/html.js';
import { useAction, useResource } from '../lib/hooks.js';
import { Page, Notice, Loading, Field } from '../lib/ui.js';
import { usePanel } from '../lib/panel.js';

const PROTOCOLS = { tcp: 'TCP', udp: 'UDP' };

export default function RulePage() {
  const { api, params } = usePanel();
  const id = params.id;
  const resource = useResource(signal => id ? api.get(`/rules/${id}`, { signal }) : Promise.resolve({}), [id]);
  if (resource.loading) return html`<${Loading} />`;
  if (resource.error) return html`<${Notice} error=${resource.error} />`;
  return html`<${RuleForm} key=${id || 'new'} rule=${resource.data} />`;
}

function RuleForm({ rule }) {
  const { api, navigate, href } = usePanel();
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
      navigate('/');
    });
  };
  return html`<${Page} title=${isNew ? 'Add rule' : `Edit ${rule.name}`} back=${{ href: href('/'), label: 'Port forwards' }}>
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
      <div class="button-row">
        <button class="button button-primary" type="submit" disabled=${busy}>${busy ? 'Saving…' : isNew ? 'Add' : 'Save'}</button>
        <a class="button" href=${href('/')}>Cancel</a>
      </div>
    </form>
  <//>`;
}
