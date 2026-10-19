<script lang="ts">
  import { onMount } from 'svelte';
  import { assistant } from '$lib/stores/assistant.svelte';
  import { Button } from '$lib/components/ui/button/index.js';
  import PlusIcon from '@lucide/svelte/icons/plus';
  import Trash2Icon from '@lucide/svelte/icons/trash-2';

  onMount(() => {
    void assistant.refreshConversations();
  });

  function formatWhen(ts?: number) {
    if (!ts) return '';
    const d = new Date(ts * 1000);
    return d.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
    });
  }
</script>

<div class="asst-history">
  <div class="asst-history-toolbar">
    <Button size="sm" variant="secondary" onclick={() => void assistant.newChat()}>
      <PlusIcon class="size-3.5" />
      New chat
    </Button>
  </div>

  {#if !assistant.conversations.length}
    <p class="asst-muted">No conversations yet.</p>
  {:else}
    <ul class="asst-history-list">
      {#each assistant.conversations as c (c.id)}
        <li class="asst-history-item" class:is-active={c.id === assistant.conversationId}>
          <button
            type="button"
            class="asst-history-main"
            onclick={() => void assistant.selectConversation(c.id)}
          >
            <span class="asst-history-title">{c.title || 'Untitled'}</span>
            {#if c.snippet}
              <span class="asst-history-snip">{c.snippet}</span>
            {/if}
            <span class="asst-history-when">{formatWhen(c.updated_at)}</span>
          </button>
          <button
            type="button"
            class="asst-history-del"
            title="Delete conversation"
            aria-label="Delete conversation"
            onclick={() => {
              if (confirm('Delete this conversation?')) void assistant.deleteConversation(c.id);
            }}
          >
            <Trash2Icon class="size-3.5" />
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>
