import { api, apiPost, API_BASE, ApiError } from '../api';
import {
  KEYS,
  getJson,
  getString,
  setJson,
  setString,
} from '../prefs';
import { live } from './live.svelte';

export type AssistantView = 'chat' | 'history' | 'settings';
export type ToolPref = 'ask' | 'always' | 'deny';

export const WRITE_TOOL_NAMES = [
  'schedule_upsert',
  'schedule_delete',
  'smart_charge_set',
  'solar_charge_set',
  'set_output',
  'apply_suggestion',
] as const;

export type WriteToolName = (typeof WRITE_TOOL_NAMES)[number];

export type ToolPrefs = Record<WriteToolName, ToolPref>;

export type AssistantMessage = {
  id: string;
  role: 'user' | 'assistant' | 'system' | string;
  content: string;
  parts?: { attachment_ids?: string[] } | null;
  created_at?: number;
};

export type PendingTool = {
  id: string;
  tool_name: string;
  args: Record<string, unknown>;
  status: string;
  message_id?: string | null;
  result?: unknown;
};

export type ConversationSummary = {
  id: string;
  title?: string;
  snippet?: string;
  updated_at?: number;
  created_at?: number;
  model?: string | null;
  provider?: string | null;
};

export type PendingAttachment = {
  id: string;
  filename: string;
  mime: string;
  size?: number;
  previewUrl?: string;
};

export type SuggestionCard = {
  id?: string | number;
  title?: string;
  summary?: string;
  reasoning?: string;
  confidence?: string;
  severity?: string;
  kind?: string;
  target?: string;
  current_value?: unknown;
  proposed_value?: unknown;
};

const DEFAULT_TOOL_PREFS: ToolPrefs = {
  schedule_upsert: 'ask',
  schedule_delete: 'ask',
  smart_charge_set: 'ask',
  solar_charge_set: 'ask',
  set_output: 'ask',
  apply_suggestion: 'ask',
};

function readToolPrefs(): ToolPrefs {
  const raw = getJson<Partial<ToolPrefs>>(KEYS.assistantToolPrefs, {});
  const out = { ...DEFAULT_TOOL_PREFS };
  for (const name of WRITE_TOOL_NAMES) {
    const v = raw[name];
    if (v === 'ask' || v === 'always' || v === 'deny') out[name] = v;
  }
  return out;
}

function readOpen(): boolean {
  return getString(KEYS.assistantOpen) === '1';
}

function readAutopilot(): boolean {
  return getString(KEYS.assistantAutopilot) === '1';
}

class AssistantStore {
  open = $state(false);
  autopilot = $state(false);
  toolPrefs = $state<ToolPrefs>({ ...DEFAULT_TOOL_PREFS });
  conversationId = $state<string | null>(null);
  conversations = $state<ConversationSummary[]>([]);
  messages = $state<AssistantMessage[]>([]);
  pendingTools = $state<PendingTool[]>([]);
  pendingAttachments = $state<PendingAttachment[]>([]);
  model = $state('');
  provider = $state('anthropic');
  models = $state<{ id: string; label: string }[]>([]);
  sending = $state(false);
  view = $state<AssistantView>('chat');
  draft = $state('');
  error = $state('');
  suggestions = $state<SuggestionCard[]>([]);
  dragging = $state(false);
  #abort: AbortController | null = null;
  #hydrated = false;

  get panelWidth() {
    return 400;
  }

  hydrate() {
    if (this.#hydrated || typeof window === 'undefined') return;
    this.#hydrated = true;
    this.open = readOpen();
    this.autopilot = readAutopilot();
    this.toolPrefs = readToolPrefs();
    this.syncBodyClass();
  }

  setOpen(on: boolean) {
    this.open = on;
    setString(KEYS.assistantOpen, on ? '1' : '0');
    this.syncBodyClass();
    if (on) {
      void this.ensureReady();
    }
  }

  toggle() {
    this.setOpen(!this.open);
  }

  setAutopilot(on: boolean) {
    this.autopilot = on;
    setString(KEYS.assistantAutopilot, on ? '1' : '0');
  }

  setToolPref(name: WriteToolName, pref: ToolPref) {
    this.toolPrefs = { ...this.toolPrefs, [name]: pref };
    setJson(KEYS.assistantToolPrefs, this.toolPrefs);
  }

  setView(v: AssistantView) {
    this.view = v;
    if (v === 'history') void this.refreshConversations();
  }

  syncBodyClass() {
    if (typeof document === 'undefined') return;
    document.body.classList.toggle('assistant-open', this.open);
    document.body.style.setProperty('--assistant-panel-w', `${this.panelWidth}px`);
  }

  async ensureReady() {
    this.hydrate();
    await Promise.all([this.loadProvider(), this.refreshSuggestions()]);
    if (!this.conversationId) {
      await this.refreshConversations();
      if (this.conversations.length) {
        await this.selectConversation(this.conversations[0].id);
      }
    }
  }

  async loadProvider() {
    try {
      const j = await api<{
        provider?: string;
        anthropic_has_key?: boolean;
        openai_has_key?: boolean;
      }>('/ai/provider');
      this.provider = j.provider || 'anthropic';
      await this.loadModels();
    } catch (err) {
      console.warn('assistant provider load failed', err);
    }
  }

  async loadModels() {
    try {
      if (this.provider === 'openai') {
        const [modelsJ, prefsJ] = await Promise.all([
          api<{ models?: { id: string; display_name?: string }[] }>('/openai/models'),
          api<{ openai_advisor_model?: string }>('/anthropic/prefs'),
        ]);
        const list = modelsJ.models || [];
        this.models = list.map((m) => ({
          id: m.id,
          label: m.display_name || m.id,
        }));
        if (!this.model) {
          this.model = prefsJ.openai_advisor_model || list[0]?.id || '';
        }
      } else {
        const [modelsJ, prefsJ] = await Promise.all([
          api<{ models?: { id: string; display_name?: string }[] }>('/anthropic/models'),
          api<{ advisor_model?: string; advisor_1m_context?: boolean }>('/anthropic/prefs'),
        ]);
        const list = modelsJ.models || [];
        this.models = list.map((m) => ({
          id: m.id,
          label: m.display_name || m.id,
        }));
        if (!this.model) {
          const base = prefsJ.advisor_model || list[0]?.id || '';
          this.model = prefsJ.advisor_1m_context && base && !base.endsWith('[1m]')
            ? `${base}[1m]`
            : base;
        }
      }
    } catch (err) {
      console.warn('assistant models load failed', err);
      this.models = [];
    }
  }

  async refreshConversations() {
    try {
      const j = await api<{ conversations?: ConversationSummary[] }>(
        '/assistant/conversations',
      );
      this.conversations = j.conversations || [];
    } catch (err) {
      console.warn('assistant conversations failed', err);
    }
  }

  async refreshSuggestions() {
    try {
      const sn = live.activeDeviceSn();
      const qs = new URLSearchParams({ status: 'pending' });
      if (sn) qs.set('device_sn', sn);
      const j = await api<{ suggestions?: SuggestionCard[] }>(
        `/algorithm/suggestions?${qs}`,
      );
      this.suggestions = j.suggestions || [];
    } catch {
      this.suggestions = [];
    }
  }

  async newChat() {
    try {
      const conv = await apiPost<{ id: string }>('/assistant/conversations', {
        title: 'New chat',
        model: this.model || null,
        provider: this.provider,
      });
      this.conversationId = conv.id;
      this.messages = [];
      this.pendingTools = [];
      this.clearAttachments();
      this.view = 'chat';
      this.error = '';
      await this.refreshConversations();
    } catch (err) {
      this.error = err instanceof Error ? err.message : 'Failed to start chat';
    }
  }

  async selectConversation(id: string) {
    try {
      const conv = await api<{
        id: string;
        messages?: AssistantMessage[];
        tool_runs?: PendingTool[];
        model?: string | null;
      }>(`/assistant/conversations/${encodeURIComponent(id)}`);
      this.conversationId = conv.id;
      this.messages = conv.messages || [];
      this.pendingTools = (conv.tool_runs || []).filter((t) => t.status === 'pending');
      if (conv.model) this.model = conv.model;
      this.clearAttachments();
      this.view = 'chat';
      this.error = '';
      await this.applyToolPrefsToPending();
    } catch (err) {
      this.error = err instanceof Error ? err.message : 'Failed to load conversation';
    }
  }

  async deleteConversation(id: string) {
    try {
      await api(`/assistant/conversations/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      });
      if (this.conversationId === id) {
        this.conversationId = null;
        this.messages = [];
        this.pendingTools = [];
      }
      await this.refreshConversations();
      if (!this.conversationId && this.conversations.length) {
        await this.selectConversation(this.conversations[0].id);
      }
    } catch (err) {
      this.error = err instanceof Error ? err.message : 'Delete failed';
    }
  }

  contextPayload() {
    const tab = live.activeTab;
    const sn = live.activeDeviceSn();
    return {
      tab,
      device_sn: sn ?? null,
      connection: live.connectionLabel,
    };
  }

  async ensureConversation(): Promise<string> {
    if (this.conversationId) return this.conversationId;
    const conv = await apiPost<{ id: string }>('/assistant/conversations', {
      title: 'New chat',
      model: this.model || null,
      provider: this.provider,
    });
    this.conversationId = conv.id;
    await this.refreshConversations();
    return conv.id;
  }

  async send(text?: string) {
    const message = (text ?? this.draft).trim();
    const attIds = this.pendingAttachments.map((a) => a.id);
    if (!message && !attIds.length) return;
    if (this.sending) return;

    this.error = '';
    this.sending = true;
    this.#abort = new AbortController();
    const optimisticId = `local-${Date.now()}`;
    if (message || attIds.length) {
      this.messages = [
        ...this.messages,
        {
          id: optimisticId,
          role: 'user',
          content: message || '(attachment)',
          parts: attIds.length ? { attachment_ids: attIds } : null,
        },
      ];
    }
    this.draft = '';

    try {
      const convId = await this.ensureConversation();
      const body = {
        conversation_id: convId,
        message,
        attachment_ids: attIds,
        tab: live.activeTab,
        context: this.contextPayload(),
        autopilot: this.autopilot,
        model: this.model || undefined,
      };
      const res = await fetch(`${API_BASE}/assistant/chat`, {
        method: 'POST',
        credentials: 'same-origin',
        cache: 'no-store',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        signal: this.#abort.signal,
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        throw new ApiError(res.status, (data as { detail?: string })?.detail || `HTTP ${res.status}`);
      }
      const msg = (data as { message?: AssistantMessage }).message;
      const pending = (data as { pending_tools?: PendingTool[] }).pending_tools || [];
      // Replace optimistic user bubble by reloading conversation messages
      await this.selectConversation(convId);
      if (msg && !this.messages.some((m) => m.id === msg.id)) {
        this.messages = [...this.messages, msg];
      }
      this.pendingTools = [
        ...this.pendingTools.filter((t) => t.status === 'pending'),
        ...pending.filter((p) => !this.pendingTools.some((t) => t.id === p.id)),
      ];
      this.clearAttachments();
      await this.refreshConversations();
      await this.applyToolPrefsToPending();
    } catch (err) {
      this.messages = this.messages.filter((m) => m.id !== optimisticId);
      if ((err as Error)?.name === 'AbortError') {
        this.error = 'Stopped.';
      } else {
        this.error = err instanceof Error ? err.message : 'Chat failed';
      }
    } finally {
      this.sending = false;
      this.#abort = null;
    }
  }

  stop() {
    this.#abort?.abort();
    this.#abort = null;
    this.sending = false;
  }

  async applyToolPrefsToPending() {
    const pending = [...this.pendingTools];
    for (const tool of pending) {
      if (tool.status !== 'pending') continue;
      const name = tool.tool_name as WriteToolName;
      if (!WRITE_TOOL_NAMES.includes(name)) continue;
      const pref = this.toolPrefs[name];
      if (pref === 'always') {
        await this.approveTool(tool.id, { silent: true });
      } else if (pref === 'deny') {
        await this.denyTool(tool.id, { silent: true });
      }
    }
  }

  async approveTool(runId: string, opts?: { silent?: boolean; always?: boolean }) {
    try {
      if (opts?.always) {
        const tool = this.pendingTools.find((t) => t.id === runId);
        if (tool && WRITE_TOOL_NAMES.includes(tool.tool_name as WriteToolName)) {
          this.setToolPref(tool.tool_name as WriteToolName, 'always');
        }
      }
      await apiPost(`/assistant/tools/${encodeURIComponent(runId)}/approve`);
      this.pendingTools = this.pendingTools.filter((t) => t.id !== runId);
    } catch (err) {
      if (!opts?.silent) {
        this.error = err instanceof Error ? err.message : 'Approve failed';
      }
    }
  }

  async denyTool(runId: string, opts?: { silent?: boolean }) {
    try {
      await apiPost(`/assistant/tools/${encodeURIComponent(runId)}/deny`);
      this.pendingTools = this.pendingTools.filter((t) => t.id !== runId);
    } catch (err) {
      if (!opts?.silent) {
        this.error = err instanceof Error ? err.message : 'Deny failed';
      }
    }
  }

  async uploadFiles(files: FileList | File[]) {
    const list = Array.from(files);
    if (!list.length) return;
    try {
      const convId = await this.ensureConversation();
      for (const file of list) {
        const fd = new FormData();
        fd.append('conversation_id', convId);
        fd.append('file', file);
        const res = await fetch(`${API_BASE}/assistant/attachments`, {
          method: 'POST',
          credentials: 'same-origin',
          body: fd,
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) {
          throw new ApiError(
            res.status,
            (data as { detail?: string })?.detail || `Upload failed (${res.status})`,
          );
        }
        const att = data as PendingAttachment;
        const previewUrl = att.mime?.startsWith('image/')
          ? `${API_BASE}/assistant/attachments/${encodeURIComponent(att.id)}`
          : undefined;
        this.pendingAttachments = [
          ...this.pendingAttachments,
          { ...att, previewUrl },
        ];
      }
    } catch (err) {
      this.error = err instanceof Error ? err.message : 'Upload failed';
    }
  }

  async removeAttachment(id: string) {
    try {
      await api(`/assistant/attachments/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      });
    } catch {
      /* still drop from UI */
    }
    this.pendingAttachments = this.pendingAttachments.filter((a) => a.id !== id);
  }

  clearAttachments() {
    for (const a of this.pendingAttachments) {
      if (a.previewUrl?.startsWith('blob:')) URL.revokeObjectURL(a.previewUrl);
    }
    this.pendingAttachments = [];
  }

  startWithSuggestion(s: SuggestionCard) {
    const title = s.title || s.target || 'suggestion';
    const summary = s.summary || s.reasoning || '';
    const id = s.id != null ? String(s.id) : '';
    const diff =
      s.current_value != null || s.proposed_value != null
        ? ` (${String(s.current_value)} → ${String(s.proposed_value)})`
        : '';
    this.draft = id
      ? `Tell me about pending suggestion #${id}: ${title}${diff}. ${summary}`.trim()
      : `Tell me about this suggestion: ${title}${diff}. ${summary}`.trim();
    this.view = 'chat';
  }
}

export const assistant = new AssistantStore();
