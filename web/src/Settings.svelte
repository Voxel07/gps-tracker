<script lang="ts">
  import { onMount } from 'svelte';
  import { api, json, message, when } from './model';
  import type { Immich, Key } from './model';

  let { immich, onClose, onImmich, onLogout }: {
    immich: Immich | null;
    onClose: () => void;
    onImmich: (value: Immich) => void;
    onLogout: () => void;
  } = $props();

  let dialog: HTMLDialogElement;
  let keys = $state<Key[]>([]);
  let name = $state('');
  let secret = $state<{ key: string; url: string } | null>(null);
  let photoKey = $state('');
  let working = $state(false);
  let error = $state('');
  let notice = $state('');
  let confirmRevoke = $state<number | null>(null);
  const body = '{"lat": %LAT, "lon": %LON, "time": "%TIME"}';

  onMount(() => {
    dialog.showModal();
    void loadKeys();
  });

  async function loadKeys() {
    try {
      keys = await api<Key[]>('/api/keys');
    } catch (err) {
      error = message(err);
    }
  }

  async function action(task: () => Promise<void>) {
    working = true;
    error = '';
    notice = '';
    try {
      await task();
    } catch (err) {
      error = message(err);
    } finally {
      working = false;
    }
  }

  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value);
      notice = 'Copied to clipboard.';
    } catch {
      notice = 'Select the text to copy it.';
    }
  }
</script>

<dialog class="drawer" bind:this={dialog} onclose={onClose} aria-labelledby="settings-title">
  <header class="drawer-header">
    <div><h2 id="settings-title">Settings</h2><p>Manage your devices and photo connection.</p></div>
    <button class="square" onclick={() => dialog.close()} aria-label="Close settings">×</button>
  </header>
  {#if error}<p class="feedback error" role="alert">{error}</p>{/if}
  {#if notice}<p class="feedback" role="status">{notice}</p>{/if}
  <section class="drawer-section">
    <h3>GPSLogger devices</h3>
    <p>Generate an upload key for each phone you want to track.</p>
    <form class="inline-form" onsubmit={(event) => {
      event.preventDefault();
      void action(async () => {
        secret = await api('/api/keys', json('POST', { name }));
        name = '';
        await loadKeys();
      });
    }}>
      <input aria-label="Device name" bind:value={name} required maxlength="60" placeholder="Device name">
      <button class="primary" disabled={working}>Generate API key</button>
    </form>
    {#if secret}
      <div class="setup">
        <strong>Set up GPSLogger</strong>
        <p>In Custom URL settings, enable logging and select POST. Copy the details below now. The key is shown once.</p>
        <div class="copy-line"><span>URL</span><code>{secret.url}</code><button onclick={() => copy(secret!.url)}>Copy</button></div>
        <div class="copy-line"><span>Headers</span><code>Authorization: Bearer {secret.key}<br>Content-Type: application/json</code><button onclick={() => copy('Authorization: Bearer ' + secret!.key + '\nContent-Type: application/json')}>Copy</button></div>
        <div class="copy-line"><span>Body</span><code>{body}</code><button onclick={() => copy(body)}>Copy</button></div>
      </div>
    {/if}
    {#each keys as key (key.id)}
      <div class="setting-row">
        <div><strong>{key.name}</strong><small>{key.revoked ? 'Revoked' : key.lastUsed ? 'Last upload ' + when(key.lastUsed * 1000) : 'Waiting for first upload'}</small></div>
        {#if !key.revoked}
          {#if confirmRevoke === key.id}
            <div class="inline-form">
              <button onclick={() => confirmRevoke = null}>Cancel</button>
              <button class="danger" disabled={working} onclick={() => action(async () => {
                await api('/api/keys/' + key.id, { method: 'DELETE' });
                confirmRevoke = null;
                secret = null;
                await loadKeys();
              })}>Confirm revoke</button>
            </div>
          {:else}<button onclick={() => confirmRevoke = key.id}>Revoke</button>{/if}
        {/if}
      </div>
    {/each}
  </section>
  <section class="drawer-section">
    <h3>Immich photos</h3>
    {#if !immich?.available}
      <p>Your server operator can enable this integration with <code>IMMICH_URL</code>.</p>
    {:else}
      <p>{immich.connected ? 'Connected to' : 'Connect to'} {immich.url} with an API key that has <code>asset.read</code> and <code>asset.view</code> permissions.</p>
      <form class="inline-form" onsubmit={(event) => {
        event.preventDefault();
        void action(async () => {
          await api('/api/immich', json('PUT', { key: photoKey }));
          photoKey = '';
          onImmich(await api<Immich>('/api/immich'));
          notice = 'Immich key saved.';
        });
      }}>
        <input type="password" autocomplete="off" aria-label="Immich API key" placeholder="Immich API key" bind:value={photoKey} required>
        <button disabled={working}>{immich.connected ? 'Replace key' : 'Test and connect'}</button>
      </form>
      {#if immich.connected}
        <button class="plain" disabled={working} onclick={() => action(async () => {
          await api('/api/immich', { method: 'DELETE' });
          onImmich(await api<Immich>('/api/immich'));
        })}>Disconnect Immich</button>
      {/if}
    {/if}
  </section>
  <footer class="drawer-footer">
    <button onclick={() => action(async () => {
      await api('/auth/logout', { method: 'POST' });
      onLogout();
    })} disabled={working}>Sign out</button>
  </footer>
</dialog>
