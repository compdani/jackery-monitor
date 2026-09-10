<script lang="ts">
  import { onMount } from 'svelte';
  import { feature } from './api';
  type Slot = { start_hour: number; end_hour: number; rate: number; label: string; months?: number[] };
  type Plan = { type: 'flat' | 'tou'; currency: string; rate_per_kwh: number; tou_rates?: Slot[] };
  let plan = $state<Plan>({ type: 'flat', currency: 'USD', rate_per_kwh: 0.3 });
  let slots = $state<{ start_hour: number; end_hour: number; rate: number; label: string; monthsText: string }[]>([]);
  let busy = $state(true);
  let error = $state('');
  let notice = $state('');
  let disposed = false;
  async function load() { try { const result = await feature<{ plan: Plan }>('/cost/plan'); if (disposed) return; plan = result.plan; slots = (plan.tou_rates ?? []).map(s => ({ ...s, monthsText: (s.months ?? []).join(', ') })); } catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Unable to load plan'; } finally { if (!disposed) busy = false; } }
  async function save(event: SubmitEvent) {
    event.preventDefault(); busy = true; error = ''; notice = '';
    try {
      const tou_rates = slots.map(({ monthsText, ...slot }) => ({ ...slot, months: monthsText.trim() ? monthsText.split(',').map(v => Number(v.trim())) : [] }));
      if (tou_rates.some(s => s.months.some(m => !Number.isInteger(m) || m < 1 || m > 12))) throw new Error('Months must be comma-separated numbers from 1 to 12.');
      await feature('/cost/plan', { ...plan, tou_rates: plan.type === 'tou' ? tou_rates : undefined }); if (!disposed) notice = 'Electricity plan saved.';
    } catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Unable to save plan'; }
    finally { if (!disposed) busy = false; }
  }
  onMount(() => { void load(); return () => { disposed = true; }; });
</script>
<section class="card"><h3>Electricity plan</h3><p class="muted">Enter the rates from your bill. Savings credit power used at its local tariff and subtract grid charging costs. Updating rates recalculates historical savings.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}{#if notice}<p role="status">{notice}</p>{/if}
  <form onsubmit={save}><div class="row"><label>Plan type<select bind:value={plan.type}><option value="flat">Flat rate</option><option value="tou">Time of use</option></select></label><label>Currency<input bind:value={plan.currency} maxlength="8" required /></label></div>
    {#if plan.type === 'flat'}<label>Rate per kWh<input type="number" min="0" max="5" step="0.0001" bind:value={plan.rate_per_kwh} required /></label>
    {:else}<p class="muted">Hours use the installation’s timezone. End hour is exclusive; a start after the end crosses midnight. The first matching row wins. Uncovered hours have a zero rate.</p>
      {#each slots as slot, i}<fieldset><legend>Rate {i + 1}</legend><div class="row"><label>Label<input bind:value={slot.label} maxlength="48" /></label><label>Start hour<input type="number" min="0" max="24" step="1" bind:value={slot.start_hour} required /></label><label>End hour<input type="number" min="0" max="24" step="1" bind:value={slot.end_hour} required /></label><label>Rate/kWh<input type="number" min="0" max="5" step="0.0001" bind:value={slot.rate} required /></label><label>Months (blank = all)<input bind:value={slot.monthsText} placeholder="6, 7, 8, 9" /></label></div><button type="button" onclick={() => slots = slots.filter((_, index) => index !== i)}>Remove rate</button></fieldset>{/each}
      <button type="button" onclick={() => slots = [...slots, { start_hour: 0, end_hour: 24, rate: 0.3, label: '', monthsText: '' }]}>Add rate</button>
    {/if}<button class="primary" disabled={busy || (plan.type === 'tou' && !slots.length)}>Save plan</button>
  </form>
</section>
<style>.row{display:flex;gap:14px;flex-wrap:wrap}.row label{flex:1;min-width:110px}fieldset{border:1px solid #344050;border-radius:10px;margin:14px 0;padding:14px}form>button{margin:14px 10px 0 0}</style>
