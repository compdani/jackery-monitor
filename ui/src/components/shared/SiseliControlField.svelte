<script lang="ts">
  import type { SiseliControl } from '$lib/types';

  /** One Siseli inverter setting editor (switch / select / number), shared by Live and Device. */
  let {
    control: c,
    disabled = false,
    onwrite,
  }: {
    control: SiseliControl;
    disabled?: boolean;
    onwrite: (key: string, value: number) => void;
  } = $props();

  const on = $derived(!!c.value);
  const label = $derived(`${c.name || c.canonical}${c.unit ? ` (${c.unit})` : ''}`);
</script>

{#if c.kind === 'switch'}
  <label class="auto-field siseli-control siseli-switch">
    <span class="auto-label">{c.name || c.canonical}</span>
    <button
      type="button"
      class="switch"
      class:on
      aria-pressed={on}
      {disabled}
      onclick={() => onwrite(c.canonical, Number(on ? c.off_value : c.on_value))}
    >
      <span class="sw-state">{on ? 'on' : 'off'}</span>
    </button>
  </label>
{:else if c.kind === 'select'}
  <label class="auto-field siseli-control">
    <span class="auto-label">{label}</span>
    <select {disabled} onchange={(e) => onwrite(c.canonical, Number(e.currentTarget.value))}>
      {#each c.options || [] as o (o.value)}
        <option value={o.value} selected={Number(o.value) === Number(c.value)}>{o.label}</option>
      {/each}
    </select>
  </label>
{:else}
  <label class="auto-field siseli-control">
    <span class="auto-label">{label}</span>
    <input
      type="number"
      min={c.min}
      max={c.max}
      step={c.step ?? 1}
      value={c.value ?? ''}
      {disabled}
      onchange={(e) => onwrite(c.canonical, Number(e.currentTarget.value))}
    />
  </label>
{/if}
