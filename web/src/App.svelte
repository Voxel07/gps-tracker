<script lang="ts">
  import Icon from './Icon.svelte';
  import { onMount } from 'svelte';
  import brandMark from './tracker.svg';
  import RouteMap from './RouteMap.svelte';
  import ProfilePage from './ProfilePage.svelte';
  import PhotoLightbox from './PhotoLightbox.svelte';
  import {
    adjacent,
    api,
    applyTheme,
    calendarRange,
    emptyRoute,
    formatDistance,
    getInitialMapLayer,
    getInitialTheme,
    heading,
    json,
    localDateTime,
    localDay,
    mapRouteColors,
    message,
    onSessionUpdate,
    rangeQuery,
    redirectToSignIn,
    refreshSession,
    saveMapLayer,
    when,
    whenShort,
  } from './model';
  import type {
    Edge,
    Immich,
    ImportSummary,
    MapLayerId,
    Photo,
    Point,
    Range,
    RecentUpload,
    Route,
    SessionInfo,
    Theme,
    View,
    Waypoint,
  } from './model';

  const views: View[] = ['Day', 'Month', 'Year', 'Custom'];
  const initialDay = calendarRange('Day', new Date());
  const initialTheme = getInitialTheme();

  let theme = $state<Theme>(initialTheme);
  let mapLayer = $state<MapLayerId>(getInitialMapLayer());
  const routeColors = $derived(mapRouteColors(mapLayer, theme));

  let user = $state<{ name: string } | null>(null);
  let sessionExpiresAt = $state<number | null>(null);
  let sessionReady = $state(false);
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
  let lightboxIndex = $state<number | null>(null);
  let immich = $state<Immich | null>(null);
  let recentUploads = $state<RecentUpload[]>([]);
  let recentLoading = $state(false);
  let profileOpen = $state(false);
  let focusPoint = $state<{ lat: number; lon: number; seq: number } | null>(null);
  let focusSeq = 0;

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
  let confirmDelete = $state(false);
  let pendingEdits = $state<Record<number, Waypoint>>({});
  let waypoints = $state<Waypoint[]>([]);

  const selected = $derived(view === 'Custom' ? custom : calendarRange(view, anchor));
  const range = $derived(editRange ?? selected);
  const rangeKey = $derived(rangeQuery(range));
  const editing = $derived(editRange !== null);
  const current = $derived(view !== 'Custom' && calendarRange(view, new Date()).from === selected.from);

  const pendingEditIds = $derived(Object.keys(pendingEdits).map(Number));
  const pendingEditCount = $derived(pendingEditIds.length);
  const hasUnsavedWork = $derived(pendingEditCount > 0 || (adding && edge !== null && waypoints.length > 0));

  const selectedDraft = $derived.by(() => {
    if (!selectedPoint) return null;
    return pendingEdits[selectedPoint.id] ?? { lat: selectedPoint.lat, lon: selectedPoint.lon, time: selectedPoint.time };
  });

  const selectedNeighbors = $derived.by(() => {
    if (!selectedPoint) return { prev: null as Point | null, next: null as Point | null };
    const idx = route.points.findIndex((p) => p.id === selectedPoint!.id);
    if (idx < 0) return { prev: null, next: null };
    const candidatePrev = route.points[idx - 1] ?? null;
    const candidateNext = route.points[idx + 1] ?? null;
    return {
      prev: candidatePrev && candidatePrev.stream === selectedPoint.stream && candidatePrev.time < selectedPoint.time ? candidatePrev : null,
      next: candidateNext && candidateNext.stream === selectedPoint.stream && selectedPoint.time < candidateNext.time ? candidateNext : null,
    };
  });

  function toggleTheme() {
    const next: Theme = theme === 'dark' ? 'light' : 'dark';
    theme = next;
    applyTheme(next);
  }

  function changeLayer(next: MapLayerId) {
    mapLayer = next;
    saveMapLayer(next);
  }

  function focusOn(lat: number, lon: number) {
    focusSeq += 1;
    focusPoint = { lat, lon, seq: focusSeq };
  }

  onMount(() => {
    applyTheme(theme);
    onSessionUpdate((updated) => {
      if (updated) {
        user = updated.user;
        sessionExpiresAt = updated.expiresAt;
      } else {
        user = null;
        sessionExpiresAt = null;
        route = emptyRoute;
        photos = [];
        immich = null;
        recentUploads = [];
        profileOpen = false;
        if (navigator.onLine) redirectToSignIn();
      }
    });

    const onNetwork = () => {
      offline = !navigator.onLine;
      reload++;
      if (!offline) void loadSession();
    };
    const onVisibility = () => {
      if (document.visibilityState === 'visible' && user && !offline && sessionExpiresAt) {
        if (sessionExpiresAt - Date.now() < 2 * 60_000) {
          void refreshSession();
        }
      }
    };
    offline = !navigator.onLine;
    if (!offline) void loadSession();
    else sessionReady = true;

    window.addEventListener('online', onNetwork);
    window.addEventListener('offline', onNetwork);
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      window.removeEventListener('online', onNetwork);
      window.removeEventListener('offline', onNetwork);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  });

  // Proactively refresh the short-lived session token 60s before expiry
  $effect(() => {
    if (!user || !sessionExpiresAt || offline) return;
    const delay = Math.max(5000, sessionExpiresAt - Date.now() - 60_000);
    const timer = window.setTimeout(() => {
      void refreshSession();
    }, delay);
    return () => window.clearTimeout(timer);
  });

  async function loadSession() {
    try {
      const state = await api<SessionInfo>('/api/session');
      offline = false;
      if (state.user) {
        user = state.user;
        sessionExpiresAt = state.expiresAt;
      } else if (state.refreshable) {
        const refreshed = await refreshSession();
        if (refreshed) {
          user = refreshed.user;
          sessionExpiresAt = refreshed.expiresAt;
        } else {
          user = null;
          sessionExpiresAt = null;
        }
      } else {
        user = null;
        sessionExpiresAt = null;
      }
      if (!user) {
        // The server only serves this page to signed-in users. Landing here
        // without a session means it ended, so go straight back to Authentik.
        redirectToSignIn();
        return;
      }
      if (user) {
        const [immichState] = await Promise.all([
          api<Immich>('/api/immich'),
          loadRecentUploads(),
        ]);
        immich = immichState;
      }
    } catch (err) {
      if (err instanceof TypeError) offline = true;
      else if (!offline) signinError = message(err);
    } finally {
      sessionReady = true;
    }
  }

  async function loadRecentUploads() {
    if (!user || offline) return;
    recentLoading = true;
    try {
      recentUploads = await api<RecentUpload[]>('/api/points/recent');
    } catch {
      // Non-fatal; keep previous list
    } finally {
      recentLoading = false;
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
    if (!detail) route = emptyRoute;
    void api<Route>('/api/route?' + query + (detail ? '&detail=1' : ''), { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          route = result;
          // Keep displayedRangeKey independent of `detail` so entering/exiting edit mode never resets zoom
          displayedRangeKey = query;
        }
      })
      .catch((err) => { if (!controller.signal.aborted) routeError = message(err); })
      .finally(() => { if (!controller.signal.aborted) loading = false; });
    return () => controller.abort();
  });

  $effect(() => {
    const query = rangeKey;
    const active = !!user && !!immich?.connected && !offline && !editing && route.count > 0;
    photos = [];
    photoError = '';
    photoPartial = false;
    lightboxIndex = null;
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
    confirmDelete = false;
    pendingEdits = {};
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

  function setPointDraft(point: Point, next: Waypoint) {
    const unchanged =
      Math.abs(next.lat - point.lat) <= 1e-9 &&
      Math.abs(next.lon - point.lon) <= 1e-9 &&
      next.time === point.time;
    const copy = { ...pendingEdits };
    if (unchanged) {
      delete copy[point.id];
    } else {
      copy[point.id] = next;
    }
    pendingEdits = copy;
  }

  function selectCapturedPoint(point: Point) {
    if (saving) return;
    selectedPoint = point;
    confirmDelete = false;
    editError = '';
  }

  function redistributeWaypointTimes(items: Waypoint[], activeEdge: Edge): Waypoint[] {
    const { left, right } = activeEdge;
    return items.map((point, i) => ({
      ...point,
      time: left.time + Math.floor(((right.time - left.time) * (i + 1)) / (items.length + 1)),
    }));
  }

  function placeWaypoint(lat: number, lon: number) {
    if (!edge || saving || waypoints.length >= 100) return;
    const next = [...waypoints, { lat, lon, time: 0 }];
    if (edge.right.time - edge.left.time <= next.length) {
      editError = 'There is no room for another recording time between these points.';
      return;
    }
    waypoints = redistributeWaypointTimes(next, edge);
    editError = '';
  }

  function addMidpoint() {
    if (!edge || saving || waypoints.length >= 100) return;
    const leftPos = pendingEdits[edge.left.id] ?? edge.left;
    const rightPos = pendingEdits[edge.right.id] ?? edge.right;
    const prev = waypoints.length > 0 ? waypoints[waypoints.length - 1] : leftPos;
    const lat = (prev.lat + rightPos.lat) / 2;
    const deltaLon = ((rightPos.lon - prev.lon + 540) % 360) - 180;
    const lon = ((prev.lon + deltaLon / 2 + 540) % 360) - 180;
    placeWaypoint(lat, lon);
  }

  function removeWaypoint(index: number) {
    if (!edge) return;
    const remaining = waypoints.filter((_, i) => i !== index);
    waypoints = redistributeWaypointTimes(remaining, edge);
    editError = '';
  }

  function startInsertBetween(left: Point, right: Point) {
    adding = true;
    edge = { left, right };
    waypoints = [];
    selectedPoint = null;
    confirmDelete = false;
    editError = '';
  }

  async function commitPendingWork(exitAfter: boolean) {
    const idsToSave = Object.keys(pendingEdits).map(Number);
    const hasWaypoints = adding && edge !== null && waypoints.length > 0;
    if (idsToSave.length === 0 && !hasWaypoints) {
      if (exitAfter) {
        editRange = null;
        resetDrafts();
      }
      return;
    }
    saving = true;
    editError = '';
    try {
      const pointById = new Map(route.points.map((p) => [p.id, p]));
      let savedPoints = 0;
      for (const id of idsToSave) {
        const original = pointById.get(id);
        const edit = pendingEdits[id];
        if (!original || !edit) continue;
        await api('/api/points/' + id, json('PATCH', {
          lat: edit.lat,
          lon: edit.lon,
          time: edit.time,
          revision: original.revision,
        }));
        savedPoints++;
      }
      let addedCount = 0;
      if (hasWaypoints && edge) {
        await api('/api/points', json('POST', {
          after: edge.left.id,
          before: edge.right.id,
          points: waypoints,
        }));
        addedCount = waypoints.length;
      }
      resetDrafts();
      if (exitAfter) editRange = null;
      if (savedPoints > 0 && addedCount > 0) {
        feedback = `Saved ${savedPoints} edited and ${addedCount} added points.`;
      } else if (savedPoints > 0) {
        feedback = savedPoints === 1 ? 'Point updated.' : `Saved ${savedPoints} edited points.`;
      } else if (addedCount > 0) {
        feedback = addedCount === 1 ? 'Added 1 point.' : `Added ${addedCount} points.`;
      }
      reload++;
    } catch (err) {
      editError = message(err);
    } finally {
      saving = false;
    }
  }

  async function saveSingleAction(request: () => Promise<unknown>, notice: string) {
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
    profileOpen = false;
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

  function jumpToUpload(item: RecentUpload) {
    editRange = null;
    resetDrafts();
    view = 'Day';
    anchor = new Date(item.recordedAt);
    feedback = 'Viewing ' + item.deviceName + ' fix from ' + whenShort(item.recordedAt) + '.';
    focusOn(item.lat, item.lon);
  }

  function openPhotoLightbox(photo: Photo) {
    const idx = photos.findIndex((p) => p.id === photo.id);
    if (idx >= 0) lightboxIndex = idx;
  }
</script>

<div class="app">
  <header class="topbar">
    <a class="brand" href="/" aria-label="GPS Tracker home">
      <img src={brandMark} alt="" width="34" height="34">
      <span>GPS <b>Tracker</b></span>
    </a>
    <div class="top-actions">
      <button type="button" class="top-button icon-pill" onclick={toggleTheme} aria-label="Toggle theme" title="Toggle dark/light theme">
        <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
        <span class="hide-mobile">{theme === 'dark' ? 'Light' : 'Dark'}</span>
      </button>
      {#if user && !offline}
        <button
          type="button"
          class="profile-trigger"
          disabled={editing}
          onclick={() => profileOpen = true}
          aria-label="Open profile, settings and import"
        >
          <span class="avatar" aria-hidden="true">{user.name.slice(0, 1).toUpperCase()}</span>
          <span class="profile-name hide-mobile">{user.name}</span>
        </button>
      {/if}
    </div>
  </header>

  <main class="workspace">
    <aside class="sidebar" class:welcome={!user || offline} aria-label="Route controls">
      {#if offline}
        <h1>You’re offline</h1>
        <p class="muted">Reconnect to view your routes and photos. GPSLogger can keep recording while your phone is offline.</p>
        <button class="primary sign-in" onclick={loadSession}>Retry connection</button>
      {:else if !user}
        <h1>Movement history</h1>
        <p class="muted">View your GPSLogger routes, edit points, fill gaps, and browse Immich photos from places you visited.</p>
        {#if signinError}
          <p class="feedback error" role="alert">{signinError}</p>
          <button class="primary sign-in" onclick={loadSession}>Try again</button>
        {:else if !sessionReady}
          <p class="muted">Checking sign-in…</p>
        {:else}
          <p class="muted">Redirecting to Authentik…</p>
          <a class="primary sign-in" href="/auth/login">Sign in with Authentik <Icon name="external" size={15} /></a>
        {/if}
      {:else}
        <h1>Movement history</h1>
        <div class="range-tabs" aria-label="Date range">
          {#each views as option}
            <button type="button" class:active={view === option} aria-pressed={view === option} disabled={editing} onclick={() => selectView(option)}>{option}</button>
          {/each}
        </div>
        <div class="date-nav">
          {#if view !== 'Custom'}
            <button type="button" class="square" aria-label={'Previous ' + view.toLowerCase()} disabled={editing} onclick={() => {
              if (view !== 'Custom') anchor = adjacent(view, anchor, -1);
            }}><Icon name="left" /></button>
          {/if}
          <div class="period">
            <strong>{heading(view, selected)}</strong>
            {#if current}<small>{view === 'Day' ? 'Today' : 'This ' + view.toLowerCase()}</small>{/if}
          </div>
          {#if view !== 'Custom'}
            <button type="button" class="square" aria-label={'Next ' + view.toLowerCase()} disabled={editing || current} onclick={() => {
              if (view !== 'Custom') anchor = adjacent(view, anchor, 1);
            }}><Icon name="right" /></button>
          {/if}
        </div>
        {#if view === 'Custom' && !editing}
          <form class="range-form" onsubmit={applyCustom}>
            <div><label>Start date<input type="date" required bind:value={fromDate}></label><label>Time<input type="time" bind:value={fromTime}></label></div>
            <div><label>End date<input type="date" required bind:value={toDate}></label><label>Time<input type="time" bind:value={toTime}></label></div>
            <button class="primary">Apply range</button>
          </form>
        {/if}

        {#if feedback}<p class="feedback" role="status">{feedback}</p>{/if}
        {#if routeError}<p class="feedback error" role="alert">{routeError}</p>{/if}
        {#if editError}<p class="feedback error" role="alert">{editError}</p>{/if}

        {#if editing}
          <section class="panel">
            <div class="section-heading">
              <h2><Icon name="edit" />Edit route</h2>
              <button
                type="button"
                class:primary={hasUnsavedWork}
                onclick={() => void commitPendingWork(true)}
                disabled={saving}
              >{saving ? 'Saving…' : 'Done'}</button>
            </div>
            <p class="muted">{heading('Custom', range)}</p>

            {#if hasUnsavedWork || (adding && edge !== null)}
              <div class="edit-top-bar">
                <button
                  type="button"
                  disabled={saving}
                  onclick={resetDrafts}
                >Cancel</button>
                {#if hasUnsavedWork}
                  <button
                    type="button"
                    class="primary"
                    disabled={saving}
                    onclick={() => void commitPendingWork(false)}
                  >
                    {#if adding && waypoints.length > 0 && pendingEditCount === 0}
                      Save {waypoints.length} {waypoints.length === 1 ? 'point' : 'points'}
                    {:else if pendingEditCount > 0 && (!adding || waypoints.length === 0)}
                      Save {pendingEditCount} {pendingEditCount === 1 ? 'point' : 'points'}
                    {:else}
                      Save all changes
                    {/if}
                  </button>
                {/if}
              </div>
            {/if}

            <div class="mode-tabs" role="tablist" aria-label="Edit mode">
              <button
                type="button"
                role="tab"
                aria-selected={!adding}
                class:active={!adding}
                disabled={saving}
                onclick={() => { adding = false; edge = null; waypoints = []; editError = ''; }}
              >Edit points</button>
              <button
                type="button"
                role="tab"
                aria-selected={adding}
                class:active={adding}
                disabled={loading || saving || route.points.length < 2}
                onclick={() => { adding = true; selectedPoint = null; confirmDelete = false; editError = ''; }}
              >Add between</button>
            </div>

            {#if adding}
              {#if !edge}
                <p>Tap any route segment (solid or dashed) or tap an existing point on the map to insert new points between two fixes.</p>
              {:else}
                <div class="anchor-box">
                  <div><small>From</small><strong>{whenShort(edge.left.time)}</strong></div>
                  <div><small>To</small><strong>{whenShort(edge.right.time)}</strong></div>
                </div>
                <p>Tap the map in order to place points, drag placed markers to fine-tune, or insert an automatic midpoint.</p>
                <div class="edit-actions">
                  <button type="button" disabled={saving || waypoints.length >= 100} onclick={addMidpoint}>+ Insert midpoint</button>
                  <button type="button" disabled={saving} onclick={() => { edge = null; waypoints = []; editError = ''; }}>Change section</button>
                </div>
                {#each waypoints as waypoint, i}
                  <div class="waypoint-card">
                    <div class="waypoint-head">
                      <strong>New point {i + 1}</strong>
                      <button type="button" class="mini-remove" aria-label={'Remove point ' + (i + 1)} onclick={() => removeWaypoint(i)}><Icon name="close" /></button>
                    </div>
                    <label class="waypoint-field">
                      Time
                      <input
                        type="datetime-local"
                        step="1"
                        value={localDateTime(waypoint.time)}
                        onchange={(event) => {
                          const value = +new Date(event.currentTarget.value);
                          if (Number.isFinite(value)) waypoints[i].time = value;
                        }}
                      >
                    </label>
                    <div class="coord-grid">
                      <label class="waypoint-field">
                        Lat
                        <input
                          type="number"
                          step="0.000001"
                          min="-90"
                          max="90"
                          value={waypoint.lat.toFixed(6)}
                          onchange={(event) => {
                            const val = Number(event.currentTarget.value);
                            if (Number.isFinite(val) && val >= -90 && val <= 90) waypoints[i].lat = val;
                          }}
                        >
                      </label>
                      <label class="waypoint-field">
                        Lon
                        <input
                          type="number"
                          step="0.000001"
                          min="-180"
                          max="180"
                          value={waypoint.lon.toFixed(6)}
                          onchange={(event) => {
                            const val = Number(event.currentTarget.value);
                            if (Number.isFinite(val) && val >= -180 && val <= 180) waypoints[i].lon = val;
                          }}
                        >
                      </label>
                    </div>
                  </div>
                {/each}
              {/if}
            {:else}
              <p>Tap any point on the map and drag it to move the point and attached route directly. Clicking Done saves all edited points.</p>
              {#if selectedPoint && selectedDraft}
                <div class="point-editor">
                  <div class="point-badges">
                    <span class="badge-soft">{selectedPoint.estimated ? 'Estimated' : 'Captured'}</span>
                    {#if selectedPoint.corrected}<span class="badge-connected">Edited</span>{/if}
                    {#if selectedPoint.id in pendingEdits}<span class="badge-connected">Unsaved</span>{/if}
                  </div>
                  <div class="coord-grid">
                    <label class="waypoint-field">
                      Latitude
                      <input
                        type="number"
                        step="0.000001"
                        min="-90"
                        max="90"
                        value={selectedDraft.lat.toFixed(6)}
                        onchange={(event) => {
                          const val = Number(event.currentTarget.value);
                          if (Number.isFinite(val) && val >= -90 && val <= 90 && selectedPoint && selectedDraft) {
                            setPointDraft(selectedPoint, { ...selectedDraft, lat: val });
                          }
                        }}
                      >
                    </label>
                    <label class="waypoint-field">
                      Longitude
                      <input
                        type="number"
                        step="0.000001"
                        min="-180"
                        max="180"
                        value={selectedDraft.lon.toFixed(6)}
                        onchange={(event) => {
                          const val = Number(event.currentTarget.value);
                          if (Number.isFinite(val) && val >= -180 && val <= 180 && selectedPoint && selectedDraft) {
                            setPointDraft(selectedPoint, { ...selectedDraft, lon: val });
                          }
                        }}
                      >
                    </label>
                  </div>
                  <label class="waypoint-field">
                    Recording time
                    <input
                      type="datetime-local"
                      step="1"
                      value={localDateTime(selectedDraft.time)}
                      onchange={(event) => {
                        const val = +new Date(event.currentTarget.value);
                        if (Number.isFinite(val) && selectedPoint && selectedDraft) {
                          setPointDraft(selectedPoint, { ...selectedDraft, time: val });
                        }
                      }}
                    >
                  </label>

                  <div class="edit-actions">
                    {#if selectedPoint.id in pendingEdits}
                      <button
                        type="button"
                        disabled={saving}
                        onclick={() => {
                          if (selectedPoint) {
                            setPointDraft(selectedPoint, { lat: selectedPoint.lat, lon: selectedPoint.lon, time: selectedPoint.time });
                          }
                        }}
                      >Reset point</button>
                    {/if}
                    {#if selectedPoint.corrected}
                      <button
                        type="button"
                        disabled={saving}
                        onclick={() => saveSingleAction(() => api('/api/points/' + selectedPoint!.id, json('PATCH', {
                          revert: true, revision: selectedPoint!.revision,
                        })), 'Original point restored.')}
                      >Restore original</button>
                    {/if}
                    {#if confirmDelete}
                      <button type="button" disabled={saving} onclick={() => confirmDelete = false}>Cancel</button>
                      <button
                        type="button"
                        class="danger"
                        disabled={saving}
                        onclick={() => saveSingleAction(() => api('/api/points/' + selectedPoint!.id + '?revision=' + selectedPoint!.revision, {
                          method: 'DELETE',
                        }), 'Point deleted.')}
                      >Confirm delete</button>
                    {:else}
                      <button type="button" class="danger" disabled={saving} onclick={() => confirmDelete = true}>Delete point</button>
                    {/if}
                  </div>

                  {#if selectedNeighbors.prev || selectedNeighbors.next}
                    <div class="insert-between-quick">
                      <small class="muted">Insert points between:</small>
                      <div class="edit-actions">
                        {#if selectedNeighbors.prev}
                          <button type="button" disabled={saving} onclick={() => startInsertBetween(selectedNeighbors.prev!, selectedPoint!)}>← Before this</button>
                        {/if}
                        {#if selectedNeighbors.next}
                          <button type="button" disabled={saving} onclick={() => startInsertBetween(selectedPoint!, selectedNeighbors.next!)}>After this →</button>
                        {/if}
                      </div>
                    </div>
                  {/if}
                </div>
              {/if}
            {/if}
          </section>
        {:else}
          <section class="panel">
            <div class="section-heading">
              <h2><Icon name="route" />Route summary</h2>
              <button type="button" disabled={loading || route.count === 0} onclick={beginEdit}><Icon name="edit" size={14} />Edit route</button>
            </div>
            {#if loading}
              <p class="muted" role="status">Loading route…</p>
            {:else if route.count === 0}
              <p class="muted">No points recorded in this period.</p>
            {:else}
              <div class="stats">
                <div><strong>{route.count.toLocaleString()}</strong><small>Points</small></div>
                <div><strong>{formatDistance(route.distance)}</strong><small>Distance</small></div>
                <div><strong>{route.latest ? new Date(route.latest.time).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }) : '–'}</strong><small>Latest</small></div>
              </div>
            {/if}
          </section>

          {#if immich?.connected && (photos.length > 0 || photoError)}
            <section class="panel">
              <div class="section-heading">
                <h2><Icon name="photo" />Immich photos ({photos.length})</h2>
                <small class="muted">Tap for preview</small>
              </div>
              {#if photoError}<p class="feedback error">{photoError}</p>{/if}
              {#if photoPartial}<p class="muted">Choose a shorter range to see more photos.</p>{/if}
              <div class="photo-strip">
                {#each photos as photo, idx (photo.id)}
                  <button
                    type="button"
                    class="photo-thumb-btn"
                    title={when(photo.time)}
                    onclick={() => lightboxIndex = idx}
                  >
                    <img
                      src={'/api/photos/' + encodeURIComponent(photo.id) + '/thumbnail'}
                      alt={'Photo from ' + when(photo.time)}
                      loading="lazy"
                    >
                    <span>{new Date(photo.time).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}</span>
                  </button>
                {/each}
              </div>
            </section>
          {/if}
        {/if}
      {/if}
    </aside>

    <section class="map-area" aria-label="Map">
      <RouteMap
        route={offline || !user ? emptyRoute : route}
        photos={offline || !user ? [] : photos}
        fitKey={displayedRangeKey}
        {mapLayer}
        {routeColors}
        {focusPoint}
        editing={editing && !offline}
        {adding}
        {edge}
        {pendingEdits}
        {selectedPoint}
        {waypoints}
        onSelect={selectCapturedPoint}
        onMove={(point, lat, lon) => {
          if (!saving) {
            selectedPoint = point;
            const existing = pendingEdits[point.id];
            setPointDraft(point, { lat, lon, time: existing ? existing.time : point.time });
          }
        }}
        onEdge={(value) => edge = value}
        onPlace={placeWaypoint}
        onMoveWaypoint={(index, lat, lon) => {
          if (waypoints[index]) {
            waypoints[index] = { ...waypoints[index], lat, lon };
          }
        }}
        onPhotoOpen={openPhotoLightbox}
        onChangeLayer={changeLayer}
      />
    </section>
  </main>
</div>

{#if profileOpen && user && !offline}
  <ProfilePage
    {user}
    {immich}
    selectedRange={selected}
    {recentUploads}
    {recentLoading}
    onClose={() => profileOpen = false}
    onImmich={(value) => immich = value}
    onImported={imported}
    onRefreshRecent={() => void loadRecentUploads()}
    onJumpToUpload={jumpToUpload}
    onLogout={() => {
      user = null;
      sessionExpiresAt = null;
      route = emptyRoute;
      photos = [];
      immich = null;
      recentUploads = [];
      profileOpen = false;
      location.replace('/auth/signed-out');
    }}
  />
{/if}

{#if lightboxIndex !== null && photos.length > 0}
  <PhotoLightbox
    {photos}
    index={lightboxIndex}
    onClose={() => lightboxIndex = null}
    onSelectIndex={(next) => lightboxIndex = next}
    onLocate={(photo) => focusOn(photo.lat, photo.lon)}
  />
{/if}
