<script lang="ts">
  import { getContext } from 'svelte';
  import { goto } from '$app/navigation';
  import AuthScreen from '../../components/auth/AuthScreen.svelte';

  const markSignedIn = getContext<(name: string) => void>('markSignedIn');

  function onAuthenticated(name: string) {
    markSignedIn?.(name);
    void goto('/');
  }
</script>

<AuthScreen mode="setup" {onAuthenticated} />
