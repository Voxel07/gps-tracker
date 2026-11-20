<script lang="ts">
  import { onMount } from 'svelte';
  import RouteMap from './RouteMap.svelte';
  import Settings from './Settings.svelte';
  import Import from './Import.svelte';
  import { adjacent, api, calendarRange, emptyRoute, heading, json, localDateTime, localDay, message, rangeQuery, when } from './model';
  import type { Edge, Immich, ImportSummary, Photo, Point, Range, Route, View, Waypoint } from './model';

  const views: View[] = ['Day', 'Month', 'Year', 'Custom'];
  const initialDay = calendarRange('Day', new Date());
  let user = $state<{ name: string } | null>(null);
  let sessionReady = $state(false);
  let signInAvailable = $state(false);
  let offline = $state(false);
  let view = $state<View>('Day');
  let anchor = $state(new Date());
  let custom = $state<Range>(initialDay);
  let fromDate = $state(localDay(initialDay.from));
  let toDate = $state(localDay(initialDay.to - 1));
  let fromTime = $state('');
  let toTime = $state('');
  let route = $state<Route>(emptyRoute);
  let photos = $state<Photo[]>([]);
  let photoPartial = $state(false);
  let photoError = $state('');
  let showPhotos = $state(true);
  let immich = $state<Immich | null>(null);
  let drawer = $state<'settings' | 'import' | null>(null);
  let loading = $state(false);
  let saving = $state(false);
  let feedback = $state('');
  let routeError = $state('');
  let editError = $state('');
  let signinError = $state('');
  let reload = $state(0);
  let displayedRangeKey = $state('');
  let editRange = $state<Range | null>(null);
  let adding = $state(false);
  let edge = $state<Edge | null>(null);
  let selectedPoint = $state<Point | null>(null);
  let draft = $state<Waypoint | null>(null);
  let waypoints = $state<Waypoint[]>([]);

  const selected = $derived(view === 'Custom' ? custom : calendarRange(view, anchor));
  const range = $derived(editRange ?? selected);
  const rangeKey = $derived(rangeQuery(range));
  const editing = $derived(editRange !== null);
  const current = $derived(view !== 'Custom' && calendarRange(view, new Date()).from === selected.from);

  onMount(() => {
    const state = () => {
      offline = !navigator.onLine;
      reload++;
      if (!offline) void loadSession();
    };
    offline = !navigator.onLine;
    if (new URL(location.href).searchParams.get('signin') === 'failed') signinError = 'Sign-in was interrupted. Try again.';
    if (!offline) void loadSession();
    else sessionReady = true;
    window.addEventListener('online', state);
    window.addEventListener('offline', state);
    return () => {
      window.removeEventListener('online', state);
      window.removeEventListener('offline', state);
    };
  });

  async function loadSession() {
    try {
      const state = await api<{ user: { name: string } | null; signInAvailable: boolean }>('/api/session');
      offline = false;
      user = state.user;
      signInAvailable = state.signInAvailable;
      if (state.user) immich = await api<Immich>('/api/immich');
    } catch (err) {
      if (err instanceof TypeError) offline = true;
      else if (!offline) signinError = message(err);
    } finally {
      sessionReady = true;
    }
  }

  $effect(() => {
    const query = rangeKey;
    const detail = editing;
    void reload;
    if (!user || offline) return;
    const controller = new AbortController();
    loading = true;
    routeError = '';
    route = emptyRoute;
    void api<Route>('/api/route?' + query + (detail ? '&detail=1' : ''), { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          route = result;
          displayedRangeKey = query + ':' + detail;
        }
      })
      .catch((err) => { if (!controller.signal.aborted) routeError = message(err); })
      .finally(() => { if (!controller.signal.aborted) loading = false; });
    return () => controller.abort();
  });

  $effect(() => {
    const query = rangeKey;
    const active = !!user && !!immich?.connected && showPhotos && !offline && !editing && route.count > 0;
    photos = [];
    photoError = '';
    photoPartial = false;
    if (!active) return;
    const controller = new AbortController();
    void api<{ photos: Photo[]; partial: boolean }>('/api/photos?' + query, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          photos = result.photos;
          photoPartial = result.partial;
        }
      })
      .catch((err) => { if (!controller.signal.aborted) photoError = message(err); });
    return () => controller.abort();
  });

  function resetDrafts() {
    adding = false;
    edge = null;
    waypoints = [];
    selectedPoint = null;
    draft = null;
    editError = '';
  }

  function selectView(next: View) {
    view = next;
    anchor = new Date();
    feedback = '';
  }

  function applyCustom(event: SubmitEvent) {
    event.preventDefault();
    const start = +new Date(fromDate + 'T' + (fromTime || '00:00'));
    const end = toTime ? +new Date(toDate + 'T' + toTime) + 60000 : calendarRange('Day', new Date(toDate + 'T00:00')).to;
    if (!Number.isFinite(start) || !Number.isFinite(end) || start >= end) {
      feedback = 'The end of the range must follow its start.';
      return;
    }
    custom = { from: start, to: end };
    feedback = '';
  }

  function beginEdit() {
    resetDrafts();
    if (route.count <= 4000) {
      editRange = selected;
    } else if (route.latest) {
      const day = calendarRange('Day', new Date(route.latest.time));
      editRange = { from: Math.max(day.from, selected.from), to: Math.min(day.to, selected.to) };
    }
  }

  function placeWaypoint(lat: number, lon: number) {
    if (!edge || saving || waypoints.length >= 100) return;
    const next = [...waypoints, { lat, lon, time: 0 }];
    const { left, right } = edge;
    if (right.time - left.time <= next.length) {
      editError = 'There is no room for another recording time between these points.';
      return;
    }
    waypoints = next.map((point, i) => ({
      ...point, time: left.time + Math.floor((right.time - left.time) * (i + 1) / (next.length + 1)),
    }));
    editError = '';
  }

  async function saveEdit(request: () => Promise<unknown>, notice: string) {
    saving = true;
    editError = '';
    try {
      await request();
      resetDrafts();
      feedback = notice;
      reload++;
    } catch (err) {
      editError = message(err);
    } finally {
      saving = false;
    }
  }

  function imported(summary: ImportSummary) {
    drawer = null;
    if (summary.from !== null && summary.to !== null) {
      fromDate = localDay(summary.from);
      toDate = localDay(summary.to);
      fromTime = '';
      toTime = '';
      custom = { from: calendarRange('Day', new Date(summary.from)).from, to: calendarRange('Day', new Date(summary.to)).to };
      view = 'Custom';
    }
    feedback = 'Imported ' + summary.added.toLocaleString() + (summary.added === 1 ? ' point.' : ' points.');
    reload++;
  }
</script>

<svelte:head><meta name="color-scheme" content="light"></svelte:head>

<div class="app">
  <header class="topbar">
    <a class="brand" href="/" aria-label="GPS Tracker home"><img src="/icons/icon-192.png" alt="" width="34" height="34"><span>GPS Tracker</span></a>
    <div class="top-actions">
      {#if user && !offline}
        <button class="top-button" disabled={editing} onclick={() => drawer = 'import'}><span aria-hidden="true">↑</span> Import</button>
        <button class="top-button" disabled={editing} onclick={() => drawer = 'settings'}><span aria-hidden="true">⚙</span> Settings</button>
        <span class="avatar" title={user.name} aria-label={user.name}>{user.name.slice(0, 1).toUpperCase()}</span>
      {/if}
    </div>
  </header>

  <main class="workspace">
    <aside class="sidebar" aria-label="Route controls">
      {#if offline}
        <h1>You’re offline</h1>
        <p class="muted">Reconnect to view your routes and photos. GPSLogger can keep recording while your phone is offline.</p>
        <button class="sign-in" onclick={loadSession}>Retry connection</button>
      {:else if !user}
        <h1>Movement history</h1>
        <p class="muted">View your GPSLogger routes, fill gaps, and find photos from places you visited.</p>
        {#if signinError}<p class="feedback error" role="alert">{signinError}</p>{/if}
        {#if !sessionReady}
          <p class="muted">Checking sign-in…</p>
        {:else if signInAvailable}
          <a class="primary sign-in" href="/auth/login">Sign in with Authentik <span aria-hidden="true">↗</span></a>
        {:else}
          <p class="feedback">Authentik sign-in is unavailable. Ask your server operator to check the configuration.</p>
        {/if}
      {:else}
        <h1>Movement history</h1>
        <div class="range-tabs" aria-label="Date range">
          {#each views as option}
            <button class:active={view === option} aria-pressed={view === option} disabled={editing} onclick={() => selectView(option)}>{option}</button>
          {/each}
        </div>
        <div class="date-nav">
          {#if view !== 'Custom'}
            <button class="square" aria-label={'Previous ' + view.toLowerCase()} disabled={editing} onclick={() => {
              if (view !== 'Custom') anchor = adjacent(view, anchor, -1);
            }}>‹</button>
          {/if}
          <div class="period">
            <strong>{heading(view, selected)}</strong>
            {#if current}<small>{view === 'Day' ? 'Today' : 'This ' + view.toLowerCase()}</small>{/if}
          </div>
          {#if view !== 'Custom'}
            <button class="square" aria-label={'Next ' + view.toLowerCase()} disabled={editing || current} onclick={() => {
              if (view !== 'Custom') anchor = adjacent(view, anchor, 1);
            }}>›</button>
          {/if}
        </div>
        {#if view === 'Custom' && !editing}
          <form class="range-form" onsubmit={applyCustom}>
            <div><label>Start date<input type="date" required bind:value={fromDate}></label><label>Time<input type="time" bind:value={fromTime}></label></div>
            <div><label>End date<input type="date" required bind:value={toDate}></label><label>Time<input type="time" bind:value={toTime}></label></div>
            <button>Apply range</button>
          </form>
        {/if}
        {#if feedback}<p class="feedback" role="status">{feedback}</p>{/if}
        {#if routeError}<p class="feedback error" role="alert">{routeError}</p>{/if}
        {#if editError}<p class="feedback error" role="alert">{editError}</p>{/if}
        {#if editing}
          <section class="panel">
            <div class="section-heading"><h2>Edit route</h2><button onclick={() => { editRange = null; resetDrafts(); }} disabled={saving}>Done</button></div>
            <p class="muted">{heading('Custom', range)}</p>
            {#if adding}
              <p>{edge ? 'Tap the map to place waypoints in order.' : 'Tap a dashed gap or the point just before the gap.'}</p>
              {#each waypoints as waypoint, i}
                <label class="waypoint-field">Waypoint {i + 1}
                  <input type="datetime-local" step="1" value={localDateTime(waypoint.time)} onchange={(event) => {
                    const value = +new Date(event.currentTarget.value);
                    if (Number.isFinite(value)) waypoints[i].time = value;
                  }}>
                </label>
              {/each}
              <div class="edit-actions">
                <button disabled={saving} onclick={resetDrafts}>Cancel</button>
                {#if waypoints.length > 0}
                  <button class="primary" disabled={saving} onclick={() => {
                    if (edge) void saveEdit(() => api('/api/points', json('POST', {
                      after: edge!.left.id, before: edge!.right.id, points: waypoints,
                    })), 'Waypoints saved.');
                  }}>Save waypoints</button>
                {/if}
              </div>
            {:else}
              <p>Drag a marker to correct its position, or add waypoints between existing fixes.</p>
              {#if selectedPoint}
                <p class="muted">{when(selectedPoint.time)}{selectedPoint.estimated ? ' · Estimated point' : ''}</p>
                <div class="edit-actions">
                  {#if draft}
                    <button disabled={saving} onclick={resetDrafts}>Cancel</button>
                    <button class="primary" disabled={saving} onclick={() => saveEdit(() => api('/api/points/' + selectedPoint!.id, json('PATCH', {
                      ...draft, revision: selectedPoint!.revision,
                    })), 'Point saved.')}>Save point</button>
                  {:else if selectedPoint.corrected}
                    <button disabled={saving} onclick={() => saveEdit(() => api('/api/points/' + selectedPoint!.id, json('PATCH', {
                      revert: true, revision: selectedPoint!.revision,
                    })), 'Original position restored.')}>Restore original position</button>
                  {/if}
                </div>
              {/if}
              <button class="full" disabled={loading || saving || draft !== null || route.points.length < 2} onclick={() => { resetDrafts(); adding = true; }}>Add waypoints</button>
            {/if}
          </section>
        {:else}
          <section class="panel">
            <div class="section-heading"><h2>Route summary</h2><button disabled={loading || route.count === 0} onclick={beginEdit}>Edit route</button></div>
            {#if loading}
              <p class="muted" role="status">Loading route…</p>
            {:else if route.count === 0}
              <p class="muted">No points recorded in this period.</p>
            {:else}
              <div class="stats">
                <div><strong>{route.count.toLocaleString()}</strong><small>Points</small></div>
                <div><strong>{route.latest ? new Date(route.latest.time).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }) : '–'}</strong><small>Latest point</small></div>
              </div>
            {/if}
          </section>
          <section class="layers">
            <h2>Map layers</h2>
            <div class="legend-row"><span class="swatch" aria-hidden="true"></span>Recorded route</div>
            <div class="legend-row"><span class="swatch dashed" aria-hidden="true"></span>Recording gap</div>
            {#if immich?.connected}
              <label class="legend-row"><span class="photo-swatch" aria-hidden="true"></span>Immich photos <input type="checkbox" bind:checked={showPhotos}></label>
              {#if photoError}<p class="feedback error">{photoError}</p>{/if}
              {#if photoPartial}<p class="muted">Choose a shorter range to see more photos.</p>{/if}
            {/if}
          </section>
        {/if}
      {/if}
    </aside>

    <section class="map-area" aria-label="Map">
      <RouteMap route={offline || !user ? emptyRoute : route} photos={offline || !user ? [] : photos}
        fitKey={displayedRangeKey} editing={editing && !offline} {adding} {edge} {draft} {selectedPoint} {waypoints}
        onSelect={(point) => { if (!draft || selectedPoint?.id === point.id) selectedPoint = point; }}
        onMove={(point, lat, lon) => { if (!saving) { selectedPoint = point; draft = { lat, lon, time: point.time }; } }}
        onEdge={(value) => edge = value} onPlace={placeWaypoint} />
    </section>
  </main>
</div>

{#if drawer === 'settings' && user && !offline}
  <Settings {immich} onClose={() => drawer = null} onImmich={(value) => immich = value}
    onLogout={() => { user = null; route = emptyRoute; immich = null; drawer = null; }} />
{:else if drawer === 'import' && user && !offline}
  <Import onClose={() => drawer = null} onImported={imported} />
{/if}
