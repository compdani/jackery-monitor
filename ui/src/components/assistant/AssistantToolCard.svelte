<script lang="ts">
  import { assistant, type PendingTool } from '$lib/stores/assistant.svelte';
  import { Button } from '$lib/components/ui/button/index.js';

  interface Props {
    tool: PendingTool;
  }
  let { tool }: Props = $props();

  let busy = $state(false);

  const argsPreview = $derived.by(() => {
    try {
      return JSON.stringify(tool.args ?? {}, null, 2);
    } catch {
      return String(tool.args);
    }
  });

  async function onApprove() {
    busy = true;
    try {
      await assistant.approveTool(tool.id);
    } finally {
      busy = false;
    }
  }

  async function onDeny() {
    busy = true;
    try {
      await assistant.denyTool(tool.id);
    } finally {
      busy = false;
    }
  }

  async function onAlways() {
    busy = true;
    try {
      await assistant.approveTool(tool.id, { always: true });
    } finally {
      busy = false;
    }
  }
</script>

<div class="asst-tool-card" data-tool={tool.tool_name}>
  <div class="asst-tool-card-head">
    <span class="asst-tool-name">{tool.tool_name}</span>
    <span class="asst-tool-status">{tool.status}</span>
  </div>
  {#if argsPreview && argsPreview !== '{}'}
    <pre class="asst-tool-args">{argsPreview}</pre>
  {/if}
  <div class="asst-tool-actions">
    <Button size="sm" variant="default" disabled={busy} onclick={onApprove}>Approve</Button>
    <Button size="sm" variant="outline" disabled={busy} onclick={onDeny}>Deny</Button>
    <Button size="sm" variant="secondary" disabled={busy} onclick={onAlways}>Always allow</Button>
  </div>
</div>
