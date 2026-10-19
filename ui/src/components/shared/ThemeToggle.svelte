<script lang="ts">
  import SunIcon from '@lucide/svelte/icons/sun';
  import MoonIcon from '@lucide/svelte/icons/moon';
  import MonitorIcon from '@lucide/svelte/icons/monitor';
  import { mode, setMode, userPrefersMode } from 'mode-watcher';
  import { buttonVariants } from '$lib/components/ui/button/index.js';
  import * as DropdownMenu from '$lib/components/ui/dropdown-menu/index.js';
  import { cn } from '$lib/utils.js';

  type ThemeChoice = 'light' | 'dark' | 'system';

  const current = $derived((userPrefersMode.current ?? 'system') as ThemeChoice);
  const resolvedDark = $derived(mode.current === 'dark');
</script>

<DropdownMenu.Root>
  <DropdownMenu.Trigger
    class={cn(buttonVariants({ variant: 'ghost', size: 'icon' }))}
    title="Theme"
    aria-label="Theme"
  >
    {#if resolvedDark}
      <MoonIcon class="size-4" />
    {:else}
      <SunIcon class="size-4" />
    {/if}
  </DropdownMenu.Trigger>
  <DropdownMenu.Content align="end" class="min-w-36">
    <DropdownMenu.Label>Theme</DropdownMenu.Label>
    <DropdownMenu.Separator />
    <DropdownMenu.RadioGroup
      value={current}
      onValueChange={(v) => {
        if (v === 'light' || v === 'dark' || v === 'system') setMode(v);
      }}
    >
      <DropdownMenu.RadioItem value="light" class="gap-2">
        <SunIcon class="size-4" />
        Light
      </DropdownMenu.RadioItem>
      <DropdownMenu.RadioItem value="dark" class="gap-2">
        <MoonIcon class="size-4" />
        Dark
      </DropdownMenu.RadioItem>
      <DropdownMenu.RadioItem value="system" class="gap-2">
        <MonitorIcon class="size-4" />
        System
      </DropdownMenu.RadioItem>
    </DropdownMenu.RadioGroup>
  </DropdownMenu.Content>
</DropdownMenu.Root>
