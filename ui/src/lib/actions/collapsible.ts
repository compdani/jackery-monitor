import type { Action } from 'svelte/action';
import { collapseKey, getString, setString } from '../prefs';

export type CollapsibleOptions = {
  /** Storage key; matches the legacy `data-collapse-key` names. */
  key: string;
  /** Initial state until the user toggles it once. */
  collapsed?: boolean;
};

/**
 * Makes a `section.card` collapsible from its `.card-header`, remembering
 * the state per browser (`jackery-collapse-<key>`, same as the legacy UI).
 * Clicks on buttons/inputs inside the header don't toggle.
 */
export const collapsible: Action<HTMLElement, CollapsibleOptions> = (node, opts) => {
  let key = opts.key;
  const header = node.querySelector<HTMLElement>(':scope > .card-header');
  if (!header) return;

  const chevron = document.createElement('span');
  chevron.className = 'collapsible-chevron';
  chevron.setAttribute('aria-hidden', 'true');
  chevron.textContent = '▾';
  header.appendChild(chevron);

  node.classList.add('collapsible');
  header.classList.add('collapsible-header');
  header.setAttribute('role', 'button');
  header.tabIndex = 0;

  const apply = (collapsed: boolean) => {
    node.classList.toggle('collapsed', collapsed);
    header.setAttribute('aria-expanded', String(!collapsed));
  };
  apply(getString(collapseKey(key), opts.collapsed ? '1' : '0') === '1');

  const toggle = () => {
    const next = !node.classList.contains('collapsed');
    apply(next);
    setString(collapseKey(key), next ? '1' : '0');
  };
  const onClick = (e: MouseEvent) => {
    if ((e.target as HTMLElement).closest('button, input, select, a, label')) return;
    toggle();
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.target !== header || (e.key !== 'Enter' && e.key !== ' ')) return;
    e.preventDefault();
    toggle();
  };
  header.addEventListener('click', onClick);
  header.addEventListener('keydown', onKey);

  return {
    update(next) {
      key = next.key;
    },
    destroy() {
      header.removeEventListener('click', onClick);
      header.removeEventListener('keydown', onKey);
      chevron.remove();
    },
  };
};
