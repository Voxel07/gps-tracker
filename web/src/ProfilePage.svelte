<script lang="ts">
  import { onMount } from 'svelte';
  import Import from './Import.svelte';
  import { api, calendarRange, downloadGPX, formatCoords, json, localDay, message, when, whenShort } from './model';
  import type { Immich, ImportSummary, Key, Range, RecentUpload, Theme } from './model';

  type Tab = 'all' | 'devices' | 'import' | 'immich';

  let {
    user,
    immich,
    theme,
    selectedRange,
    recentUploads,
    recentLoading,
    onClose,
    onImmich,
    onImported,
    onToggleTheme,
    onRefreshRecent,
    onJumpToUpload,
    onLogout,
  }: {
    user: { name: string };
    immich: Immich | null;
    theme: Theme;
    selectedRange: Range;
    recentUploads: RecentUpload[];
    recentLoading: boolean;
    onClose: () => void;
    onImmich: (value: Immich) => void;
    onImported: (summary: ImportSummary) => void;
    onToggleTheme: () => void;
    onRefreshRecent: () => void;
    onJumpToUpload: (item: RecentUpload) => void;
    onLogout: () => void;
  } = $props();

  const KEY_MASK = '********************';

  let tab = $state<Tab>('all');
  let keys = $state<Key[]>([]);
  let name = $state('');
  let secret = $state<{ key: string; url: string } | null>(null);
  let photoKey = $state('');
  let editingPhotoKey = $state(false);
  let working = $state(false);
  let error = $state('');
  let notice = $state('');
  let confirmRevoke = $state<number | null>(null);
  let exportFromDate = $state('');
  let exportToDate = $state('');
  let exportFromTime = $state('');
  let exportToTime = $state('');
  let exporting = $state(false);
  const body = '{"lat": %LAT, "lon": %LON, "time": "%TIME"}';

  const showingSavedMask = $derived(!!immich?.connected && !editingPhotoKey && photoKey === '');

  function setExportDates(range: Range) {
    exportFromDate = localDay(range.from);
    exportToDate = localDay(Math.max(range.from, range.to - 1));
    exportFromTime = '';
    exportToTime = '';
  }

  function applyPreset(preset: 'view' | 'today' | 'week' | 'month' | 'year') {
    const now = new Date();
    if (preset === 'view') {
      setExportDates(selectedRange);
    } else if (preset === 'today') {
      setExportDates(calendarRange('Day', now));
    } else if (preset === 'week') {
      const today = calendarRange('Day', now);
      const start = new Date(today.from);
      start.setDate(start.getDate() - 6);
      setExportDates({ from: +start, to: today.to });
    } else if (preset === 'month') {
      setExportDates(calendarRange('Month', now));
    } else {
      setExportDates(calendarRange('Year', now));
    }
  }

  async function handleExport(event: SubmitEvent) {
    event.preventDefault();
    const start = +new Date(exportFromDate + 'T' + (exportFromTime || '00:00'));
    const end = exportToTime
      ? +new Date(exportToDate + 'T' + exportToTime) + 60000
      : calendarRange('Day', new Date(exportToDate + 'T00:00')).to;
    if (!Number.isFinite(start) || !Number.isFinite(end) || start >= end) {
      error = 'The end of the export range must follow its start.';
      return;
    }
    exporting = true;
    error = '';
    notice = '';
    try {
      const filename = await downloadGPX({ from: start, to: end });
      notice = 'Exported ' + filename + '.';
    } catch (err) {
      error = message(err);
    } finally {
      exporting = false;
    }
  }

  onMount(() => {
    setExportDates(selectedRange);
    void loadKeys();
    onRefreshRecent();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
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

<div
  class="profile-backdrop"
  role="dialog"
  aria-modal="true"
  aria-labelledby="profile-title"
  tabindex="-1"
  onclick={(e) => { if (e.target === e.currentTarget) onClose(); }}
  onkeydown={(e) => { if (e.key === 'Escape') onClose(); }}
>
  <div class="profile-page">
    <header class="profile-header">
      <div class="profile-identity">
        <span class="profile-avatar" aria-hidden="true">{user.name.slice(0, 1).toUpperCase()}</span>
        <div>
          <h2 id="profile-title">{user.name}</h2>
          <p class="muted">Settings, device keys, imports & recent uploads</p>
        </div>
      </div>
      <div class="profile-header-actions">
        <button type="button" onclick={onToggleTheme} aria-label="Toggle color theme">
          {theme === 'dark' ? '☀ Light' : '☾ Dark'}
        </button>
        <button
          type="button"
          class="danger"
          disabled={working}
          onclick={() => action(async () => {
            await api('/auth/logout', { method: 'POST' });
            onLogout();
          })}
        >Sign out</button>
        <button type="button" class="square" onclick={onClose} aria-label="Close profile">×</button>
      </div>
    </header>

    <nav class="profile-tabs" aria-label="Profile sections">
      <button type="button" class:active={tab === 'all'} onclick={() => tab = 'all'}>All</button>
      <button type="button" class:active={tab === 'devices'} onclick={() => tab = 'devices'}>Devices & History</button>
      <button type="button" class:active={tab === 'import'} onclick={() => tab = 'import'}>Import & Export</button>
      <button type="button" class:active={tab === 'immich'} onclick={() => tab = 'immich'}>Immich & Theme</button>
    </nav>

    {#if error}<p class="feedback error" role="alert">{error}</p>{/if}
    {#if notice}<p class="feedback" role="status">{notice}</p>{/if}

    <div class="profile-grid" class:single={tab !== 'all'}>
      {#if tab === 'all' || tab === 'import'}
        <section class="profile-card">
          <div class="card-head">
            <div>
              <h3>Import GPS data</h3>
              <p class="muted">Test or import GPX tracks and GPSLogger CSV exports with live progress.</p>
            </div>
          </div>
          <Import {onImported} />
        </section>

        <section class="profile-card">
          <div class="card-head">
            <div>
              <h3>Export GPX data</h3>
              <p class="muted">Download full-resolution GPX tracks for any date period.</p>
            </div>
          </div>
          <div class="layer-pills" aria-label="Export date presets">
            <button type="button" onclick={() => applyPreset('view')}>Current view</button>
            <button type="button" onclick={() => applyPreset('today')}>Today</button>
            <button type="button" onclick={() => applyPreset('week')}>Last 7 days</button>
            <button type="button" onclick={() => applyPreset('month')}>This month</button>
            <button type="button" onclick={() => applyPreset('year')}>This year</button>
          </div>
          <form class="range-form" onsubmit={handleExport}>
            <div>
              <label>Start date<input type="date" required bind:value={exportFromDate} disabled={exporting}></label>
              <label>Time<input type="time" bind:value={exportFromTime} disabled={exporting}></label>
            </div>
            <div>
              <label>End date<input type="date" required bind:value={exportToDate} disabled={exporting}></label>
              <label>Time<input type="time" bind:value={exportToTime} disabled={exporting}></label>
            </div>
            <button type="submit" class="primary" disabled={exporting || !exportFromDate || !exportToDate}>
              {exporting ? 'Exporting GPX…' : 'Export GPX'}
            </button>
          </form>
        </section>
      {/if}

      {#if tab === 'all' || tab === 'immich'}
        <section class="profile-card">
          <div class="card-head">
            <div>
              <h3>Immich photos</h3>
              {#if !immich?.available}
                <p class="muted">Your server operator can enable this integration with <code>IMMICH_URL</code>.</p>
              {:else}
                <p class="muted">
                  {immich.connected ? 'Connected to' : 'Connect to'} <code>{immich.url}</code> with an API key that has <code>asset.read</code> and <code>asset.view</code> permissions.
                </p>
              {/if}
            </div>
            {#if immich?.connected}
              <span class="badge-connected">Connected</span>
            {/if}
          </div>

          {#if immich?.available}
            <form
              class="inline-form"
              onsubmit={(event) => {
                event.preventDefault();
                if (!photoKey.trim()) return;
                void action(async () => {
                  await api('/api/immich', json('PUT', { key: photoKey }));
                  photoKey = '';
                  editingPhotoKey = false;
                  onImmich(await api<Immich>('/api/immich'));
                  notice = 'Immich API key saved.';
                });
              }}
            >
              <input
                type={showingSavedMask ? 'text' : 'password'}
                autocomplete="off"
                aria-label="Immich API key"
                placeholder="Enter Immich API key"
                value={showingSavedMask ? KEY_MASK : photoKey}
                onfocus={() => {
                  if (showingSavedMask) {
                    editingPhotoKey = true;
                    photoKey = '';
                  }
                }}
                oninput={(event) => {
                  editingPhotoKey = true;
                  photoKey = event.currentTarget.value;
                }}
                onblur={() => {
                  if (immich?.connected && photoKey === '') {
                    editingPhotoKey = false;
                  }
                }}
                required
              >
              <button class="primary" disabled={working || !photoKey.trim()}>
                {immich.connected ? 'Replace key' : 'Test & connect'}
              </button>
            </form>
            {#if immich.connected}
              <div class="card-sub-actions">
                <small class="muted">Saved key is masked. Focus the field to enter a replacement.</small>
                <button
                  type="button"
                  class="plain danger"
                  disabled={working}
                  onclick={() => action(async () => {
                    await api('/api/immich', { method: 'DELETE' });
                    photoKey = '';
                    editingPhotoKey = false;
                    onImmich(await api<Immich>('/api/immich'));
                    notice = 'Immich disconnected.';
                  })}
                >Disconnect Immich</button>
              </div>
            {/if}
          {/if}

          <div class="theme-pref">
            <div>
              <strong>Appearance</strong>
              <small class="muted">Switch between dark (#060517) and warm light mode.</small>
            </div>
            <button type="button" onclick={onToggleTheme}>
              {theme === 'dark' ? 'Switch to Light' : 'Switch to Dark'}
            </button>
          </div>
        </section>
      {/if}

      {#if tab === 'all' || tab === 'devices'}
        <section class="profile-card">
          <div class="card-head">
            <div>
              <h3>GPSLogger devices</h3>
              <p class="muted">Generate an upload key for each phone you want to track.</p>
            </div>
          </div>

          <form
            class="inline-form"
            onsubmit={(event) => {
              event.preventDefault();
              void action(async () => {
                secret = await api('/api/keys', json('POST', { name }));
                name = '';
                await loadKeys();
              });
            }}
          >
            <input aria-label="Device name" bind:value={name} required maxlength="60" placeholder="Device name (e.g. Pixel 9)">
            <button class="primary" disabled={working}>Generate key</button>
          </form>

          {#if secret}
            <div class="setup">
              <strong>Set up GPSLogger</strong>
              <p class="muted">In Custom URL settings, enable logging and select POST. Copy these details now — the key is only shown once.</p>
              <div class="copy-line"><span>URL</span><code>{secret.url}</code><button type="button" onclick={() => copy(secret!.url)}>Copy</button></div>
              <div class="copy-line"><span>Headers</span><code>Authorization: Bearer {secret.key}<br>Content-Type: application/json</code><button type="button" onclick={() => copy('Authorization: Bearer ' + secret!.key + '\nContent-Type: application/json')}>Copy</button></div>
              <div class="copy-line"><span>Body</span><code>{body}</code><button type="button" onclick={() => copy(body)}>Copy</button></div>
            </div>
          {/if}

          <div class="key-list">
            {#if keys.length === 0}
              <p class="muted">No device keys created yet.</p>
            {/if}
            {#each keys as key (key.id)}
              <div class="setting-row">
                <div>
                  <div class="key-title">
                    <strong>{key.name}</strong>
                    {#if !key.revoked}<code class="key-mask" aria-label="API key active">************</code>{/if}
                  </div>
                  <small>{key.revoked ? 'Revoked' : key.lastUsed ? 'Last upload ' + when(key.lastUsed * 1000) : 'Waiting for first upload'}</small>
                </div>
                {#if !key.revoked}
                  {#if confirmRevoke === key.id}
                    <div class="inline-form">
                      <button type="button" onclick={() => confirmRevoke = null}>Cancel</button>
                      <button
                        type="button"
                        class="danger"
                        disabled={working}
                        onclick={() => action(async () => {
                          await api('/api/keys/' + key.id, { method: 'DELETE' });
                          confirmRevoke = null;
                          secret = null;
                          await loadKeys();
                        })}
                      >Confirm revoke</button>
                    </div>
                  {:else}
                    <button type="button" onclick={() => confirmRevoke = key.id}>Revoke</button>
                  {/if}
                {/if}
              </div>
            {/each}
          </div>
        </section>

        <section class="profile-card">
          <div class="card-head">
            <div>
              <h3>Last 10 points sent to app</h3>
              <p class="muted">Most recent fixes received from your GPSLogger devices.</p>
            </div>
            <button type="button" disabled={recentLoading} onclick={onRefreshRecent}>
              {recentLoading ? 'Refreshing…' : 'Refresh'}
            </button>
          </div>

          {#if recentUploads.length === 0}
            <p class="muted">No device uploads recorded yet.</p>
          {:else}
            <div class="recent-list">
              {#each recentUploads as item (item.id)}
                <div class="recent-row">
                  <div class="recent-info">
                    <div class="recent-top">
                      <strong>{item.deviceName}</strong>
                      <span class:dup={item.duplicate} class="status-pill">{item.duplicate ? 'Duplicate' : 'Saved'}</span>
                    </div>
                    <small class="mono">{formatCoords(item.lat, item.lon)}</small>
                    <small class="muted">Recorded {whenShort(item.recordedAt)} · Received {whenShort(item.receivedAt)}</small>
                  </div>
                  <button type="button" onclick={() => { onJumpToUpload(item); onClose(); }}>Map</button>
                </div>
              {/each}
            </div>
          {/if}
        </section>
      {/if}
    </div>
  </div>
</div>
