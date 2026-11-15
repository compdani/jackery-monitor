<script lang="ts">
  import { api, apiPost } from '$lib/api';
  let snapshots = $state<{ name: string; size: number; modified: number }[]>([]);
  let busy = $state(false);
  let message = $state('');
  async function list() {
    snapshots = (await api<{ snapshots: typeof snapshots }>('/backup/snapshots')).snapshots;
  }
  async function run(action: () => Promise<void>) {
    busy = true;
    try { await action(); message = 'Done'; }
    catch (e) { message = e instanceof Error ? e.message : 'Backup failed'; }
    finally { busy = false; }
  }
  async function create() { await apiPost('/backup/run', {}); await list(); }
  async function restore(name: string) {
    if (!confirm(`Restore ${name}? This replaces the current application database. The server will restart.`)) return;
    await run(async () => { await apiPost('/backup/restore', { name }); });
  }
</script>
<section class="card device-card">
  <h2>Local backups</h2>
  <p class="hint">PocketBase archives stored on this Raspberry Pi. Copy archives to separate storage for protection against disk failure.</p>
  <button class="btn btn-primary" disabled={busy} onclick={() => run(create)}>Create backup</button>
  <button class="btn btn-ghost" disabled={busy} onclick={() => run(list)}>List backups</button>
  <p role="status">{message}</p>
  {#each snapshots as snapshot (snapshot.name)}
    <p>{snapshot.name} · {snapshot.size} bytes
      <button class="btn btn-ghost" disabled={busy} onclick={() => restore(snapshot.name)}>Restore</button>
    </p>
  {/each}
</section>
