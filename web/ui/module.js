// Port forwarder entry point (panel module contract, version 1). The host
// is a panel shell, or standalone.js on its own.
import { createMount } from './lib/mount.js';

const routes = [
  { path: '/rules/new', load: () => import('./pages/rule-form.js') },
  { path: '/rules/:id/edit', load: () => import('./pages/rule-form.js') },
  { path: '/', load: () => import('./pages/rules.js') },
];

export const mount = createMount({ routes });
