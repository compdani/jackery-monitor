<script lang="ts">
  import { onMount } from 'svelte';
  import { live } from '$lib/stores/live.svelte';
  import { assistant } from '$lib/stores/assistant.svelte';
  import { Button } from '$lib/components/ui/button/index.js';
  import AssistantHistory from './AssistantHistory.svelte';
  import AssistantSettings from './AssistantSettings.svelte';
  import AssistantToolCard from './AssistantToolCard.svelte';
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
  import HistoryIcon from '@lucide/svelte/icons/history';
  import SettingsIcon from '@lucide/svelte/icons/settings';
  import PaperclipIcon from '@lucide/svelte/icons/paperclip';
  import SendIcon from '@lucide/svelte/icons/send';
  import SquareIcon from '@lucide/svelte/icons/square';
  import XIcon from '@lucide/svelte/icons/x';
  import MessageSquareIcon from '@lucide/svelte/icons/message-square';

  type MdSeg = { t: 'text' | 'bold' | 'br'; v?: string };

  const tabLabel = $derived(live.activeTab || 'live');

  const pendingForLastAssistant = $derived(
    assistant.pendingTools.filter((t) => t.status === 'pending'),
  );

  /** Scroll chat body when message list / sending state changes. */
  function autoscroll(dep: number) {
    return (node: HTMLElement) => {
      void dep;
      node.scrollTop = node.scrollHeight;
    };
  }

  onMount(() => {
    assistant.hydrate();
    if (assistant.open) void assistant.ensureReady();
  });

  /** Safe segments: plain text, **bold**, and newlines — no HTML injection. */
  function parseLiteMd(text: string): MdSeg[] {
    const out: MdSeg[] = [];
    const lines = (text || '').split('\n');
    for (let li = 0; li < lines.length; li++) {
      const line = lines[li];
      const re = /\*\*(.+?)\*\*/g;
      let last = 0;
      let m: RegExpExecArray | null;
      while ((m = re.exec(line))) {
        if (m.index > last) out.push({ t: 'text', v: line.slice(last, m.index) });
        out.push({ t: 'bold', v: m[1] });
        last = m.index + m[0].length;
      }
      if (last < line.length) out.push({ t: 'text', v: line.slice(last) });
      if (li < lines.length - 1) out.push({ t: 'br' });
    }
    return out;
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      void assistant.send();
    }
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    assistant.dragging = false;
    const files = e.dataTransfer?.files;
    if (files?.length) void assistant.uploadFiles(files);
  }

  function onFilePick(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    if (input.files?.length) void assistant.uploadFiles(input.files);
    input.value = '';
  }

  function openFilePicker() {
    const input = document.getElementById('asst-file-input') as HTMLInputElement | null;
    input?.click();
  }
</script>

{#if assistant.open}
  <aside
    class="asst-panel"
    class:is-dragging={assistant.dragging}
    aria-label="AI Assistant"
    ondragover={(e) => {
      e.preventDefault();
      assistant.dragging = true;
    }}
    ondragleave={() => {
      assistant.dragging = false;
    }}
    ondrop={onDrop}
  >
    <header class="asst-header">
      <div class="asst-header-left">
        <Button
          variant="ghost"
          size="icon"
          class="size-8 shrink-0"
          title="Collapse"
          aria-label="Collapse assistant"
          onclick={() => assistant.setOpen(false)}
        >
          <ChevronRightIcon class="size-4" />
        </Button>
        <h2 class="asst-title">AI Assistant</h2>
      </div>
      <div class="asst-header-actions">
        <Button
          variant={assistant.view === 'history' ? 'secondary' : 'ghost'}
          size="icon"
          class="size-8"
          title="History"
          aria-label="Conversation history"
          onclick={() =>
            assistant.setView(assistant.view === 'history' ? 'chat' : 'history')}
        >
          <HistoryIcon class="size-4" />
        </Button>
        <Button
          variant={assistant.view === 'settings' ? 'secondary' : 'ghost'}
          size="icon"
          class="size-8"
          title="Settings"
          aria-label="Assistant settings"
          onclick={() =>
            assistant.setView(assistant.view === 'settings' ? 'chat' : 'settings')}
        >
          <SettingsIcon class="size-4" />
        </Button>
        <select
          class="asst-select asst-model-select"
          title="Model"
          aria-label="Model"
          value={assistant.model}
          onchange={(e) => {
            assistant.model = (e.currentTarget as HTMLSelectElement).value;
          }}
        >
          {#if !assistant.models.length && assistant.model}
            <option value={assistant.model}>{assistant.model}</option>
          {/if}
          {#each assistant.models as m (m.id)}
            <option value={m.id}>{m.label}</option>
          {/each}
        </select>
      </div>
    </header>

    {#if assistant.view === 'history'}
      <div class="asst-body">
        <AssistantHistory />
      </div>
    {:else if assistant.view === 'settings'}
      <div class="asst-body">
        <AssistantSettings />
      </div>
    {:else}
      <div
        class="asst-body asst-chat"
        {@attach autoscroll(assistant.messages.length + (assistant.sending ? 1 : 0))}
      >
        {#if !assistant.messages.length}
          <div class="asst-empty">
            <MessageSquareIcon class="asst-empty-icon" />
            <p class="asst-empty-blurb">
              Ask about battery, forecast, schedules, or solar charging. I can propose
              changes — you approve write actions unless Autopilot is on.
            </p>
            {#if assistant.suggestions.length}
              <div class="asst-starter-cards">
                <h3 class="asst-starter-h">Pending suggestions</h3>
                {#each assistant.suggestions as s, i (s.id ?? i)}
                  <button
                    type="button"
                    class="asst-starter-card"
                    onclick={() => assistant.startWithSuggestion(s)}
                  >
                    <span class="asst-starter-title">{s.title || s.target || 'Suggestion'}</span>
                    {#if s.summary || s.reasoning}
                      <span class="asst-starter-sum">{s.summary || s.reasoning}</span>
                    {/if}
                  </button>
                {/each}
              </div>
            {/if}
          </div>
        {:else}
          <div class="asst-messages">
            {#each assistant.messages as m (m.id)}
              <div class="asst-msg" class:is-user={m.role === 'user'} class:is-asst={m.role === 'assistant'}>
                <div class="asst-msg-role">{m.role === 'user' ? 'You' : 'Assistant'}</div>
                <div class="asst-msg-body">
                  {#each parseLiteMd(m.content) as seg, si (`${m.id}-${si}`)}
                    {#if seg.t === 'bold'}
                      <strong>{seg.v}</strong>
                    {:else if seg.t === 'br'}
                      <br />
                    {:else}
                      {seg.v}
                    {/if}
                  {/each}
                </div>
              </div>
            {/each}

            {#if pendingForLastAssistant.length}
              <div class="asst-pending-tools">
                {#each pendingForLastAssistant as tool (tool.id)}
                  <AssistantToolCard {tool} />
                {/each}
              </div>
            {/if}

            {#if assistant.sending}
              <div class="asst-msg is-asst">
                <div class="asst-msg-role">Assistant</div>
                <div class="asst-msg-body asst-typing">Thinking…</div>
              </div>
            {/if}
          </div>
        {/if}
      </div>

      <footer class="asst-composer">
        {#if assistant.error}
          <div class="asst-error" role="alert">{assistant.error}</div>
        {/if}

        <div class="asst-context-row">
          <span class="asst-context-chip" title="Current tab context">
            {tabLabel}
          </span>
          {#if assistant.autopilot}
            <span class="asst-context-chip is-auto">Autopilot</span>
          {/if}
        </div>

        {#if assistant.pendingAttachments.length}
          <div class="asst-att-chips">
            {#each assistant.pendingAttachments as a (a.id)}
              <div class="asst-att-chip">
                {#if a.previewUrl}
                  <img src={a.previewUrl} alt="" class="asst-att-thumb" />
                {/if}
                <span class="asst-att-name" title={a.filename}>{a.filename}</span>
                <button
                  type="button"
                  class="asst-att-remove"
                  title="Remove"
                  aria-label="Remove attachment"
                  onclick={() => void assistant.removeAttachment(a.id)}
                >
                  <XIcon class="size-3" />
                </button>
              </div>
            {/each}
          </div>
        {/if}

        <div class="asst-composer-row">
          <input
            id="asst-file-input"
            type="file"
            class="sr-only"
            multiple
            accept="image/*,.pdf,.txt,.md,.csv,text/plain,text/markdown,text/csv,application/pdf"
            onchange={onFilePick}
          />
          <Button
            variant="ghost"
            size="icon"
            class="size-9 shrink-0"
            title="Attach file"
            aria-label="Attach file"
            disabled={assistant.sending}
            onclick={openFilePicker}
          >
            <PaperclipIcon class="size-4" />
          </Button>
          <textarea
            class="asst-input"
            rows="2"
            placeholder="Ask the assistant…"
            bind:value={assistant.draft}
            onkeydown={onKeydown}
            disabled={assistant.sending}
          ></textarea>
          {#if assistant.sending}
            <Button
              variant="secondary"
              size="icon"
              class="size-9 shrink-0"
              title="Stop"
              aria-label="Stop"
              onclick={() => assistant.stop()}
            >
              <SquareIcon class="size-3.5" />
            </Button>
          {:else}
            <Button
              variant="default"
              size="icon"
              class="size-9 shrink-0"
              title="Send"
              aria-label="Send"
              disabled={!assistant.draft.trim() && !assistant.pendingAttachments.length}
              onclick={() => void assistant.send()}
            >
              <SendIcon class="size-4" />
            </Button>
          {/if}
        </div>
      </footer>
    {/if}
  </aside>

  <button
    type="button"
    class="asst-backdrop"
    aria-label="Close assistant"
    onclick={() => assistant.setOpen(false)}
  ></button>
{/if}
