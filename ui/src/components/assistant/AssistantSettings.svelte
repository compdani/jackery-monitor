<script lang="ts">
  import {
    assistant,
    WRITE_TOOL_NAMES,
    type ToolPref,
    type WriteToolName,
  } from '$lib/stores/assistant.svelte';
  import { Switch } from '$lib/components/ui/switch/index.js';
  import { Label } from '$lib/components/ui/label/index.js';

  const TOOL_LABELS: Record<WriteToolName, string> = {
    schedule_upsert: 'Upsert load schedule',
    schedule_delete: 'Delete schedule window',
    smart_charge_set: 'Smart charge settings',
    solar_charge_set: 'Solar charge settings',
    set_output: 'Toggle output ports',
    apply_suggestion: 'Apply algorithm suggestion',
  };

  function onPrefChange(name: WriteToolName, e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value as ToolPref;
    if (v === 'ask' || v === 'always' || v === 'deny') {
      assistant.setToolPref(name, v);
    }
  }
</script>

<div class="asst-settings">
  <div class="asst-settings-row">
    <div class="asst-settings-copy">
      <Label for="asst-autopilot">Autopilot</Label>
      <p class="asst-muted">
        Always allow write tools during chat (sends <code>autopilot: true</code>).
      </p>
    </div>
    <Switch
      id="asst-autopilot"
      checked={assistant.autopilot}
      onCheckedChange={(v: boolean) => assistant.setAutopilot(v)}
    />
  </div>

  <h3 class="asst-settings-h">Tool permissions</h3>
  <p class="asst-muted asst-settings-lead">
    When Autopilot is off, each write tool can ask, always allow, or deny.
  </p>

  <ul class="asst-tool-prefs">
    {#each WRITE_TOOL_NAMES as name (name)}
      <li class="asst-tool-pref">
        <label class="asst-tool-pref-label" for={`asst-pref-${name}`}>
          {TOOL_LABELS[name]}
          <span class="asst-tool-pref-id">{name}</span>
        </label>
        <select
          id={`asst-pref-${name}`}
          class="asst-select"
          value={assistant.toolPrefs[name]}
          onchange={(e) => onPrefChange(name, e)}
        >
          <option value="ask">Ask</option>
          <option value="always">Always allow</option>
          <option value="deny">Deny</option>
        </select>
      </li>
    {/each}
  </ul>
</div>
