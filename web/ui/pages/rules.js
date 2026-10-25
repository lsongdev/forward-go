import { html, useState } from '../lib/html.js';
import { useAction, useResource } from '../lib/hooks.js';
import { Page, Notice, Loading, Empty, Status, Field, Modal, confirmDanger } from '../lib/ui.js';
import { usePanel } from '../lib/panel.js';
import { bytes } from '../lib/format.js';

const PROTOCOLS = { tcp: 'TCP', udp: 'UDP' };

function State({ rule }) {
  const s = rule.stats;
  if (s.running) return html`<${Status} state="running" />`;
  if (rule.enabled) return html`<${Status} state="failed" label="未运行" title=${s.error} />`;
  return html`<${Status} state="stopped" label="已停用" />`;
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
    if (!confirmDanger(`删除转发规则 ${r.name}？已建立的连接会被断开。`)) return;
    action.run(async () => { await api.del(`/rules/${r.id}`); rules.reload(); });
  };

  return html`<${Page} title="转发规则" subtitle="把本机端口的 TCP 连接或 UDP 数据报转发到其它地址。"
    actions=${html`<button class="btn btn-primary" onClick=${() => setEditing({})}>添加规则</button>`}>
    <${Notice} error=${rules.error || action.error} />
    ${rules.data === undefined ? (rules.error ? null : html`<${Loading} />`) : !rules.data.length ? html`<${Empty}>还没有转发规则。<//>` : html`
      <div class="table-wrap"><table class="table">
        <thead><tr><th>名称</th><th>转发</th><th>状态</th><th class="num">连接</th><th class="num">流量 入 / 出</th><th class="actions"></th></tr></thead>
        <tbody>${rules.data.map(r => html`<tr key=${r.id}>
          <td><div class="cell-main">${r.name}</div><div class="cell-sub">${PROTOCOLS[r.protocol]}</div></td>
          <td><span class="route mono"><span>${r.listen}</span><span class="arrow">→</span><span>${r.target}</span></span>
            ${r.enabled && r.stats.error ? html`<div class="error-text">${r.stats.error}</div>` : null}</td>
          <td><${State} rule=${r} /></td>
          <td class="num">${r.stats.running ? html`${r.stats.active}<div class="cell-sub">共 ${r.stats.total}</div>` : '—'}</td>
          <td class="num nowrap">${r.stats.running ? `${bytes(r.stats.bytes_in)} / ${bytes(r.stats.bytes_out)}` : '—'}</td>
          <td class="actions"><span class="btn-group">
            <button class="btn btn-sm" disabled=${action.busy} onClick=${() => toggle(r)}>${r.enabled ? '停用' : '启用'}</button>
            <button class="btn btn-sm" onClick=${() => setEditing(r)}>编辑</button>
            <button class="btn btn-sm btn-danger" disabled=${action.busy} onClick=${() => remove(r)}>删除</button>
          </span></td>
        </tr>`)}</tbody>
      </table></div>`}
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
  return html`<${Modal} title=${isNew ? '添加转发规则' : `编辑 ${rule.name}`} onClose=${onClose} footer=${html`
    <span class="spacer"></span>
    <button class="btn" type="button" onClick=${onClose}>取消</button>
    <button class="btn btn-primary" type="submit" form="rule-form" disabled=${busy}>${isNew ? '添加' : '保存'}</button>`}>
    <form id="rule-form" class="form" onSubmit=${submit}>
      <${Notice} error=${error} />
      <${Field} label="名称" required>
        <input class="input" required value=${r.name} onInput=${e => set({ name: e.currentTarget.value })} placeholder="nas-ssh" autofocus />
      <//>
      <fieldset class="field"><span class="field-label">协议</span><div class="choices">
        ${Object.entries(PROTOCOLS).map(([k, label]) => html`<label class="check" key=${k}>
          <input type="radio" name="protocol" checked=${r.protocol === k} onChange=${() => set({ protocol: k })} />${label}</label>`)}
      </div></fieldset>
      <div class="form-grid">
        <${Field} label="监听地址" required hint="端口，或 地址:端口；只写端口时监听所有网卡">
          <input class="input mono" required value=${r.listen} onInput=${e => set({ listen: e.currentTarget.value })} placeholder="2222" />
        <//>
        <${Field} label="目标地址" required hint="主机:端口">
          <input class="input mono" required value=${r.target} onInput=${e => set({ target: e.currentTarget.value })} placeholder="192.168.1.10:22" />
        <//>
      </div>
      <label class="check"><input type="checkbox" checked=${r.enabled} onChange=${e => set({ enabled: e.currentTarget.checked })} />保存后立即启用</label>
    </form>
  <//>`;
}
