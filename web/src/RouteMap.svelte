<script lang="ts">
  import { onMount } from 'svelte';
  import L from 'leaflet';
  import { mapLayers, when } from './model';
  import type { Edge, MapLayerId, Photo, Point, Route, Waypoint } from './model';

  type Props = {
    route: Route;
    photos: Photo[];
    fitKey: string;
    mapLayer: MapLayerId;
    showRoute: boolean;
    showGaps: boolean;
    focusPoint: { lat: number; lon: number; seq: number } | null;
    editing: boolean;
    adding: boolean;
    selectedPoint: Point | null;
    draft: Waypoint | null;
    edge: Edge | null;
    waypoints: Waypoint[];
    onSelect: (point: Point) => void;
    onMove: (point: Point, lat: number, lon: number) => void;
    onEdge: (edge: Edge) => void;
    onPlace: (lat: number, lon: number) => void;
    onMoveWaypoint: (index: number, lat: number, lon: number) => void;
    onPhotoOpen: (photo: Photo) => void;
    onChangeLayer: (id: MapLayerId) => void;
  };

  let {
    route,
    photos,
    fitKey,
    mapLayer,
    showRoute,
    showGaps,
    focusPoint,
    editing,
    adding,
    selectedPoint,
    draft,
    edge,
    waypoints,
    onSelect,
    onMove,
    onEdge,
    onPlace,
    onMoveWaypoint,
    onPhotoOpen,
    onChangeLayer,
  }: Props = $props();

  let node: HTMLDivElement;
  let map = $state.raw<L.Map>();
  let tileLayer: L.TileLayer | null = null;
  let routeLayer: L.LayerGroup;
  let photoLayer: L.LayerGroup;
  let editBaseLayer: L.LayerGroup;
  let editActiveLayer: L.LayerGroup;
  let fitted = '';
  let lastFocusSeq = 0;
  let layerMenuOpen = $state(false);

  const activeLayerOption = $derived(mapLayers.find((l) => l.id === mapLayer) ?? mapLayers[0]);

  const dot = (kind: string, size: number) => L.divIcon({ className: 'map-dot ' + kind, iconSize: [size, size] });
  const loc = (point: { lat: number; lon: number }): L.LatLngTuple => [point.lat, point.lon];
  const normalizeLon = (lon: number) => ((lon + 180) % 360 + 360) % 360 - 180;

  onMount(() => {
    const instance = L.map(node, { zoomControl: false, preferCanvas: true, scrollWheelZoom: true }).setView([50.4, 10.3], 5);
    L.control.zoom({ position: 'topright' }).addTo(instance);
    L.control.scale({ position: 'bottomleft', imperial: false }).addTo(instance);
    routeLayer = L.layerGroup().addTo(instance);
    photoLayer = L.layerGroup().addTo(instance);
    editBaseLayer = L.layerGroup().addTo(instance);
    editActiveLayer = L.layerGroup().addTo(instance);
    instance.on('click', (event: L.LeafletMouseEvent) => {
      if (layerMenuOpen) layerMenuOpen = false;
      if (editing && adding && edge) onPlace(event.latlng.lat, normalizeLon(event.latlng.lng));
    });
    const resize = new ResizeObserver(() => instance.invalidateSize({ pan: false }));
    resize.observe(node);
    map = instance;
    return () => {
      resize.disconnect();
      instance.remove();
    };
  });

  $effect(() => {
    if (!map) return;
    const config = activeLayerOption;
    if (tileLayer) {
      map.removeLayer(tileLayer);
    }
    tileLayer = L.tileLayer(config.url, {
      maxZoom: config.maxZoom,
      attribution: config.attribution,
      subdomains: config.subdomains ?? 'abc',
    }).addTo(map);
    tileLayer.bringToBack();
  });

  $effect(() => {
    if (!map || !focusPoint || focusPoint.seq === lastFocusSeq) return;
    lastFocusSeq = focusPoint.seq;
    map.setView([focusPoint.lat, focusPoint.lon], Math.max(map.getZoom(), 15), { animate: true });
  });

  function line(points: L.LatLngTuple[], color: string, dashed = false) {
    if (points.length < 2) return;
    L.polyline(points, {
      color,
      weight: dashed ? 3 : 4,
      opacity: dashed ? 0.78 : 0.94,
      dashArray: dashed ? '6 9' : undefined,
      interactive: false,
    }).addTo(routeLayer);
  }

  $effect(() => {
    if (!map) return;
    routeLayer.clearLayers();
    const points = route.points;
    const routeColor = '#cf5c36';
    const gapColor = activeLayerOption.isDark ? '#efc88b' : '#8d6442';
    let run: L.LatLngTuple[] = [];
    for (let i = 0; i < points.length; i++) {
      const point = points[i];
      const previous = points[i - 1];
      if (previous && (previous.stream !== point.stream || point.breakBefore || Math.abs(point.lon - previous.lon) > 180)) {
        if (showRoute) line(run, routeColor);
        run = [];
        if (showGaps && point.breakBefore && previous.stream === point.stream && Math.abs(point.lon - previous.lon) <= 180) {
          line([loc(previous), loc(point)], gapColor, true);
        }
      }
      run.push(loc(point));
    }
    if (showRoute) line(run, routeColor);
    if (route.latest && !editing && showRoute) {
      const label = document.createElement('span');
      label.textContent = (route.latest.estimated ? 'Estimated point · ' : 'Latest point · ') + when(route.latest.time);
      L.marker(loc(route.latest), { icon: dot('latest', 20), title: label.textContent, zIndexOffset: 300 })
        .bindPopup(label).addTo(routeLayer);
    }
    if (points.length > 0 && fitted !== fitKey) {
      fitted = fitKey;
      map.fitBounds(L.latLngBounds(points.map(loc)), { padding: [46, 46], maxZoom: 15, animate: false });
    }
  });

  $effect(() => {
    if (!map) return;
    photoLayer.clearLayers();
    if (editing) return;
    for (const photo of photos) {
      const marker = L.marker(loc(photo), {
        icon: dot(photo.estimated ? 'photo estimated' : 'photo', 26),
        title: 'Photo · ' + when(photo.time),
        zIndexOffset: 200,
      }).addTo(photoLayer);
      const body = document.createElement('div');
      body.className = 'photo-popup';
      const caption = document.createElement('p');
      caption.textContent = when(photo.time) + (photo.estimated ? ' · Approximate' : '');
      marker.bindPopup(body, { minWidth: 185 });
      marker.on('popupopen', () => {
        if (body.childElementCount) return;
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'photo-popup-btn';
        button.title = 'Open larger preview';
        const image = document.createElement('img');
        image.alt = 'Immich photo preview';
        image.src = '/api/photos/' + encodeURIComponent(photo.id) + '/thumbnail';
        image.addEventListener('load', () => marker.getPopup()?.update(), { once: true });
        image.addEventListener('error', () => { image.replaceWith('Thumbnail unavailable'); }, { once: true });
        const hint = document.createElement('span');
        hint.className = 'photo-popup-hint';
        hint.textContent = 'Tap to enlarge';
        button.append(image, hint);
        button.addEventListener('click', () => onPhotoOpen(photo));
        body.append(button, caption);
      });
    }
  });

  $effect(() => {
    if (!map) return;
    editBaseLayer.clearLayers();
    if (!editing) return;
    const pickingEdge = adding && edge === null;
    for (let i = 0; i < route.points.length; i++) {
      const point = route.points[i];
      const next = route.points[i + 1];
      const prev = route.points[i - 1];
      const isAnchor = edge && (point.id === edge.left.id || point.id === edge.right.id);
      const marker = L.circleMarker(loc(point), {
        radius: isAnchor ? 8 : 6,
        color: isAnchor ? '#060517' : '#ffffff',
        weight: isAnchor ? 3 : 2,
        fillColor: isAnchor ? '#efc88b' : (point.estimated ? '#efc88b' : '#cf5c36'),
        fillOpacity: 1,
        bubblingMouseEvents: false,
      }).addTo(editBaseLayer);
      marker.bindTooltip((point.estimated ? 'Estimated point · ' : '') + when(point.time), { direction: 'top', offset: [0, -6] });
      marker.on('click', () => {
        if (adding) {
          if (pickingEdge) {
            if (next && next.stream === point.stream && point.time < next.time) {
              onEdge({ left: point, right: next });
            } else if (prev && prev.stream === point.stream && prev.time < point.time) {
              onEdge({ left: prev, right: point });
            }
          }
        } else {
          onSelect(point);
        }
      });
      // Allow clicking ANY consecutive segment (solid OR dashed gap) in the same stream when picking where to insert points
      if (pickingEdge && next && next.stream === point.stream && point.time < next.time && Math.abs(next.lon - point.lon) <= 180) {
        const hitLine = L.polyline([loc(point), loc(next)], {
          weight: 18,
          opacity: 0,
          bubblingMouseEvents: false,
          className: 'edge-hit-target',
        });
        hitLine.bindTooltip('Tap to insert points between these fixes', { sticky: true });
        hitLine.on('click', () => onEdge({ left: point, right: next }));
        hitLine.addTo(editBaseLayer);
      }
    }
  });

  $effect(() => {
    if (!map) return;
    editActiveLayer.clearLayers();
    if (!editing) return;
    if (edge) {
      L.polyline([loc(edge.left), ...waypoints.map(loc), loc(edge.right)], {
        color: '#efc88b',
        weight: 4,
        dashArray: '6 6',
        interactive: false,
      }).addTo(editActiveLayer);
    }
    if (!adding && selectedPoint) {
      const point = selectedPoint;
      const marker = L.marker(draft ? loc(draft) : loc(point), {
        icon: dot('selected', 22),
        draggable: true,
        title: (point.estimated ? 'Estimated point · ' : '') + when(point.time),
        zIndexOffset: 500,
        autoPan: true,
      }).addTo(editActiveLayer);
      marker.on('dragend', () => {
        const position = marker.getLatLng();
        onMove(point, position.lat, normalizeLon(position.lng));
      });
    }
    waypoints.forEach((waypoint, index) => {
      const marker = L.marker(loc(waypoint), {
        icon: dot('waypoint', 20),
        draggable: true,
        title: 'New point ' + (index + 1) + ' (drag to move)',
        zIndexOffset: 450,
      }).addTo(editActiveLayer);
      marker.on('dragend', () => {
        const position = marker.getLatLng();
        onMoveWaypoint(index, position.lat, normalizeLon(position.lng));
      });
    });
  });
</script>

<div class="map-wrapper">
  <div class="leaflet-host" class:placing={editing && adding && edge !== null} bind:this={node} aria-label="GPS route map"></div>

  <div class="map-layer-control">
    <button
      type="button"
      class="layer-trigger"
      aria-expanded={layerMenuOpen}
      aria-label="Choose map layer"
      onclick={() => layerMenuOpen = !layerMenuOpen}
    >
      <span aria-hidden="true">◫</span> {activeLayerOption.shortLabel}
    </button>
    {#if layerMenuOpen}
      <div class="layer-popover" role="menu" aria-label="Base map layers">
        {#each mapLayers as option (option.id)}
          <button
            type="button"
            role="menuitemradio"
            aria-checked={mapLayer === option.id}
            class:active={mapLayer === option.id}
            onclick={() => { onChangeLayer(option.id); layerMenuOpen = false; }}
          >
            {option.label}
          </button>
        {/each}
      </div>
    {/if}
  </div>
</div>
