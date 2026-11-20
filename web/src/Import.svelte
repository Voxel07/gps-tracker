<script lang="ts">
  import { formatBytes, message, uploadImportWithProgress, when } from './model';
  import type { ImportProgressState, ImportSummary } from './model';

  let { onImported }: { onImported: (summary: ImportSummary) => void } = $props();

  let file = $state<File>();
  let summary = $state<ImportSummary>();
  let busy = $state(false);
  let activeMode = $state<'test' | 'import' | null>(null);
  let progress = $state<ImportProgressState | null>(null);
  let error = $state('');

  function stageLabel(mode: 'test' | 'import' | null, state: ImportProgressState | null): string {
    if (!state) return mode === 'import' ? 'Preparing import…' : 'Preparing test…';
    switch (state.stage) {
      case 'uploading':
        return 'Uploading file to server';
      case 'parsing':
        return 'Parsing & validating track points';
      case 'checking':
        return 'Testing against existing points';
      case 'saving':
        return 'Saving points to database';
    }
  }

  function overallPercent(state: ImportProgressState | null): number {
    if (!state) return 0;
    const pct = Math.max(0, Math.min(100, state.percent));
    switch (state.stage) {
      case 'uploading':
        return Math.round(pct * 0.3);
      case 'parsing':
        return 30 + Math.round(pct * 0.35);
      case 'checking':
      case 'saving':
        return 65 + Math.round(pct * 0.35);
    }
  }

  function stepStatus(step: 1 | 2 | 3, state: ImportProgressState | null): 'done' | 'active' | 'pending' {
    if (!state) return step === 1 ? 'active' : 'pending';
    const current = state.stage === 'uploading' ? 1 : state.stage === 'parsing' ? 2 : 3;
    if (step < current) return 'done';
    if (step === current) return state.percent >= 100 && step === 3 ? 'done' : 'active';
    return 'pending';
  }

  async function run(preview: boolean) {
    if (!file) return;
    const format = file.name.split('.').at(-1)?.toLowerCase();
    if (format !== 'gpx' && format !== 'csv') {
      error = 'Choose a GPX or GPSLogger CSV file.';
      return;
    }
    busy = true;
    activeMode = preview ? 'test' : 'import';
    error = '';
    progress = {
      stage: 'uploading',
      percent: 0,
      bytesRead: 0,
      totalBytes: file.size,
      processed: 0,
      total: 0,
      valid: 0,
      invalid: 0,
      added: 0,
      duplicates: 0,
    };
    try {
      const result = await uploadImportWithProgress(file, format, preview, (next) => {
        progress = next;
      });
      summary = result;
      if (!preview) {
        onImported(result);
      }
    } catch (err) {
      error = message(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="import-block">
  {#if error}<p class="feedback error" role="alert">{error}</p>{/if}

  <form onsubmit={(event) => { event.preventDefault(); void run(!summary); }}>
    <label class="file-field">
      <strong>Track file</strong>
      <small>GPX or GPSLogger CSV · Up to 100 MiB and 500,000 points</small>
      <input
        type="file"
        accept=".gpx,.csv"
        required
        disabled={busy}
        onchange={(event) => {
          file = event.currentTarget.files?.[0];
          summary = undefined;
          progress = null;
          activeMode = null;
          error = '';
        }}
      >
      {#if file}
        <span class="file-meta">{file.name} · {formatBytes(file.size)}</span>
      {/if}
    </label>

    {#if progress && (busy || activeMode !== null)}
      <div class="progress-card" role="status" aria-live="polite">
        <div class="progress-header">
          <div>
            <strong>{activeMode === 'import' ? 'Importing track' : 'Testing track file'}</strong>
            <small>{busy ? stageLabel(activeMode, progress) : 'Completed'}</small>
          </div>
          <span class="progress-pct">{busy ? overallPercent(progress) : 100}%</span>
        </div>

        <div class="progress-track" aria-hidden="true">
          <div class="progress-fill" style:width={(busy ? overallPercent(progress) : 100) + '%'}></div>
        </div>

        <ol class="progress-steps">
          <li class={stepStatus(1, progress)}>
            <span class="step-dot" aria-hidden="true"></span>
            <span>Upload</span>
          </li>
          <li class={stepStatus(2, progress)}>
            <span class="step-dot" aria-hidden="true"></span>
            <span>Parse</span>
          </li>
          <li class={stepStatus(3, progress)}>
            <span class="step-dot" aria-hidden="true"></span>
            <span>{activeMode === 'import' ? 'Save' : 'Validate'}</span>
          </li>
        </ol>

        <div class="progress-metrics">
          {#if progress.stage === 'uploading'}
            <span>Uploaded {formatBytes(progress.bytesRead)} / {formatBytes(progress.totalBytes)}</span>
          {:else if progress.stage === 'parsing'}
            <span>Parsed {progress.valid.toLocaleString()} valid · {progress.invalid.toLocaleString()} invalid</span>
          {:else}
            <span>
              {progress.processed.toLocaleString()} / {Math.max(progress.total, progress.valid).toLocaleString()} points
              · {progress.added.toLocaleString()} {activeMode === 'import' ? 'added' : 'new'}
              · {progress.duplicates.toLocaleString()} duplicates
            </span>
          {/if}
        </div>
      </div>
    {/if}

    {#if summary && !busy}
      <dl class="import-stats">
        <div><dt>Ready to add</dt><dd>{summary.added.toLocaleString()}</dd></div>
        <div><dt>Duplicates</dt><dd>{summary.duplicates.toLocaleString()}</dd></div>
        <div><dt>Invalid</dt><dd>{summary.invalid.toLocaleString()}</dd></div>
      </dl>
      {#if summary.from !== null && summary.to !== null}
        <p class="muted import-span">Time span: {when(summary.from)} – {when(summary.to)}</p>
      {/if}
    {/if}

    <div class="edit-actions">
      <button type="button" disabled={busy || !file} onclick={() => void run(true)}>
        {busy && activeMode === 'test' ? 'Testing…' : 'Test / Preview'}
      </button>
      <button
        type="submit"
        class="primary"
        disabled={busy || !file || (summary !== undefined && summary.added === 0)}
      >
        {busy && activeMode === 'import' ? 'Importing…' : (summary ? 'Import ' + summary.added.toLocaleString() + ' points' : 'Test file first')}
      </button>
    </div>
  </form>
</div>
