import Sortable from 'sortablejs';
import type { SortableEvent } from 'sortablejs';

export type SortableOptions = {
  handle?: string;
  animation?: number;
  ghostClass?: string;
  dragClass?: string;
  delay?: number;
  delayOnTouchOnly?: boolean;
  onEnd?: (ids: string[]) => void;
  /** Attribute used to collect ordered ids (default data-card). */
  idAttr?: string;
};

/** Svelte action wrapping SortableJS for hero-card reorder. */
export function sortable(node: HTMLElement, options: SortableOptions = {}) {
  const idAttr = options.idAttr || 'data-card';
  const instance = Sortable.create(node, {
    handle: options.handle ?? '.grab-handle',
    animation: options.animation ?? 180,
    ghostClass: options.ghostClass ?? 'sortable-ghost',
    dragClass: options.dragClass ?? 'sortable-drag',
    delay: options.delay ?? 100,
    delayOnTouchOnly: options.delayOnTouchOnly ?? true,
    touchStartThreshold: 5,
    onEnd: (_evt: SortableEvent) => {
      const ids = [...node.querySelectorAll(`[${idAttr}]`)].map(
        (el) => (el as HTMLElement).getAttribute(idAttr) || '',
      );
      options.onEnd?.(ids.filter(Boolean));
    },
  });

  return {
    destroy() {
      instance.destroy();
    },
  };
}
