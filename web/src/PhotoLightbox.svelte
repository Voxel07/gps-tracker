<script lang="ts">
  import Icon from './Icon.svelte';
  import { onMount } from 'svelte';
  import { formatCoords, when } from './model';
  import type { Photo } from './model';

  let { photos, index, onClose, onSelectIndex, onLocate }: {
    photos: Photo[];
    index: number;
    onClose: () => void;
    onSelectIndex: (next: number) => void;
    onLocate: (photo: Photo) => void;
  } = $props();

  let fallback = $state(false);
  let loaded = $state(false);
  let failed = $state(false);

  const photo = $derived(photos[index]);
  const src = $derived(
    photo
      ? '/api/photos/' + encodeURIComponent(photo.id) + '/thumbnail' + (fallback ? '' : '?size=preview')
      : '',
  );

  $effect(() => {
    void photo?.id;
    fallback = false;
    loaded = false;
    failed = false;
  });

  function prev() {
    if (photos.length <= 1) return;
    onSelectIndex((index - 1 + photos.length) % photos.length);
  }

  function next() {
    if (photos.length <= 1) return;
    onSelectIndex((index + 1) % photos.length);
  }

  onMount(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
      else if (event.key === 'ArrowLeft') prev();
      else if (event.key === 'ArrowRight') next();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });
</script>

{#if photo}
  <div class="lightbox-backdrop" role="dialog" aria-modal="true" aria-label="Immich photo preview" tabindex="-1" onclick={(e) => { if (e.target === e.currentTarget) onClose(); }} onkeydown={(e) => { if (e.key === 'Escape') onClose(); }}>
    <div class="lightbox-shell">
      <header class="lightbox-header">
        <div class="lightbox-meta">
          <strong>{when(photo.time)}</strong>
          <small>
            {formatCoords(photo.lat, photo.lon)}
            {#if photo.estimated} · <span class="badge-soft">Estimated location</span>{/if}
            {#if photos.length > 1} · {index + 1} / {photos.length}{/if}
          </small>
        </div>
        <div class="lightbox-actions">
          <button type="button" onclick={() => { onLocate(photo); onClose(); }}><Icon name="pin" />Locate on map</button>
          <button type="button" class="square" onclick={onClose} aria-label="Close preview"><Icon name="close" /></button>
        </div>
      </header>

      <div class="lightbox-stage">
        {#if photos.length > 1}
          <button type="button" class="lightbox-nav prev" onclick={prev} aria-label="Previous photo"><Icon name="left" /></button>
        {/if}

        {#if !loaded && !failed}
          <div class="lightbox-loading" role="status">Loading full preview…</div>
        {/if}

        {#if failed}
          <p class="feedback error">Could not load this Immich image preview.</p>
        {:else}
          <img
            class:ready={loaded}
            src={src}
            alt={'Immich photo from ' + when(photo.time)}
            onload={() => loaded = true}
            onerror={() => {
              if (!fallback) {
                fallback = true;
              } else {
                failed = true;
              }
            }}
          >
        {/if}

        {#if photos.length > 1}
          <button type="button" class="lightbox-nav next" onclick={next} aria-label="Next photo"><Icon name="right" /></button>
        {/if}
      </div>
    </div>
  </div>
{/if}
