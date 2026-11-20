<script lang="ts">
  import { onMount } from 'svelte';
  import { api, message, when } from './model';
  import type { ImportSummary } from './model';

  let { onClose, onImported }: { onClose: () => void; onImported: (summary: ImportSummary) => void } = $props();
  let dialog: HTMLDialogElement;
  let file = $state<File>();
  let summary = $state<ImportSummary>();
  let busy = $state(false);
  let error = $state('');
  onMount(() => dialog.showModal());

  async function submit() {
    if (!file) return;
    const format = file.name.split('.').at(-1)?.toLowerCase();
    if (format !== 'gpx' && format !== 'csv') {
      error = 'Choose a GPX or GPSLogger CSV file.';
      return;
    }
    busy = true;
    error = '';
    const preview = !summary;
    try {
      const result = await api<ImportSummary>('/api/import?format=' + format + '&preview=' + (preview ? '1' : '0'), {
        method: 'POST', body: file,
      });
      summary = result;
      if (!preview) {
        onImported(result);
        dialog.close();
      }
    } catch (err) {
      error = message(err);
    } finally {
      busy = false;
    }
  }
</script>

<dialog class="drawer" bind:this={dialog} onclose={onClose} aria-labelledby="import-title">
  <header class="drawer-header">
    <div><h2 id="import-title">Import GPS data</h2><p>Add a GPX track or a GPSLogger CSV export.</p></div>
    <button class="square" onclick={() => dialog.close()} aria-label="Close import">×</button>
  </header>
  <section class="drawer-section">
    {#if error}<p class="feedback error" role="alert">{error}</p>{/if}
    <form onsubmit={(event) => { event.preventDefault(); void submit(); }}>
      <label class="file-field">
        <strong>Track file</strong><small>GPX or CSV · Up to 100 MiB</small>
        <input type="file" accept=".gpx,.csv" required disabled={busy} onchange={(event) => {
          file = event.currentTarget.files?.[0];
          summary = undefined;
          error = '';
        }}>
      </label>
      {#if summary}
        <dl class="import-stats">
          <div><dt>Ready to add</dt><dd>{summary.added.toLocaleString()}</dd></div>
          <div><dt>Duplicates</dt><dd>{summary.duplicates.toLocaleString()}</dd></div>
          <div><dt>Invalid</dt><dd>{summary.invalid.toLocaleString()}</dd></div>
        </dl>
        {#if summary.from !== null && summary.to !== null}
          <p>{when(summary.from)} – {when(summary.to)}</p>
        {/if}
      {/if}
      <button class="primary" disabled={busy || (summary !== undefined && summary.added === 0)}>
        {busy ? (summary ? 'Importing…' : 'Reading file…') : (summary ? 'Import points' : 'Preview import')}
      </button>
    </form>
  </section>
</dialog>
