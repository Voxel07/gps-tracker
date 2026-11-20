<script lang="ts">
  import { onMount } from 'svelte';
  import L from 'leaflet';
  import type { Edge, Photo, Point, Route, Waypoint } from './model';
  import { when } from './model';

  type Props = {
    route: Route;
    photos: Photo[];
    fitKey: string;
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
  };

  let { route, photos, fitKey, editing, adding, selectedPoint, draft, edge, waypoints, onSelect, onMove, onEdge, onPlace }: Props = $props();
  let node: HTMLDivElement;
  let map = $state.raw<L.Map>();
  let routeLayer: L.LayerGroup;
  let photoLayer: L.LayerGroup;
  let editLayer: L.LayerGroup;
  let fitted = '';

  const dot = (kind: string, size: number) => L.divIcon({ className: 'map-dot ' + kind, iconSize: [size, size] });
  const loc = (point: { lat: number; lon: number }): L.LatLngTuple => [point.lat, point.lon];
  const normalizeLon = (lon: number) => ((lon + 180) % 360 + 360) % 360 - 180;

  onMount(() => {
    const instance = L.map(node, { zoomControl: false, preferCanvas: true, scrollWheelZoom: true }).setView([50.4, 10.3], 5);
    L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 19,
      attribution: '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
    }).addTo(instance);
    L.control.zoom({ position: 'topright' }).addTo(instance);
    L.control.scale({ position: 'bottomleft', imperial: false }).addTo(instance);
    routeLayer = L.layerGroup().addTo(instance);
    photoLayer = L.layerGroup().addTo(instance);
    editLayer = L.layerGroup().addTo(instance);
    instance.on('click', (event: L.LeafletMouseEvent) => {
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

  function line(points: L.LatLngTuple[], color: string, dashed = false) {
    if (points.length < 2) return;
    L.polyline(points, {
      color, weight: dashed ? 3 : 4, opacity: dashed ? 0.72 : 0.92,
      dashArray: dashed ? '6 9' : undefined, interactive: false,
    }).addTo(routeLayer);
  }

  $effect(() => {
    if (!map) return;
    routeLayer.clearLayers();
    const points = route.points;
    let run: L.LatLngTuple[] = [];
    for (let i = 0; i < points.length; i++) {
      const point = points[i];
      const previous = points[i - 1];
      if (previous && (previous.stream !== point.stream || point.breakBefore || Math.abs(point.lon - previous.lon) > 180)) {
        line(run, '#146945');
        run = [];
        if (point.breakBefore && previous.stream === point.stream && Math.abs(point.lon - previous.lon) <= 180) {
          line([loc(previous), loc(point)], '#56716a', true);
        }
      }
      run.push(loc(point));
    }
    line(run, '#146945');
    if (route.latest && !editing) {
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
        icon: dot(photo.estimated ? 'photo estimated' : 'photo', 25), title: 'Photo · ' + when(photo.time), zIndexOffset: 200,
      }).addTo(photoLayer);
      const body = document.createElement('div');
      body.className = 'photo-popup';
      const caption = document.createElement('p');
      caption.textContent = when(photo.time) + (photo.estimated ? ' · Approximate location' : '');
      marker.bindPopup(body, { minWidth: 165 });
      marker.on('popupopen', () => {
        if (body.childElementCount) return;
        const image = document.createElement('img');
        image.alt = 'Immich photo';
        image.src = '/api/photos/' + encodeURIComponent(photo.id) + '/thumbnail';
        image.addEventListener('load', () => marker.getPopup()?.update(), { once: true });
        image.addEventListener('error', () => { image.replaceWith('Thumbnail unavailable'); }, { once: true });
        body.append(image, caption);
      });
    }
  });

  $effect(() => {
    if (!map) return;
    editLayer.clearLayers();
    if (!editing) return;
    if (edge) {
      L.polyline([loc(edge.left), ...waypoints.map(loc), loc(edge.right)], {
        color: '#ad5e20', weight: 4, dashArray: '5 6', interactive: false,
      }).addTo(editLayer);
    }
    for (let i = 0; i < route.points.length; i++) {
      const point = route.points[i];
      const next = route.points[i + 1];
      const chosen = selectedPoint?.id === point.id;
      const marker = L.marker(chosen && draft ? loc(draft) : loc(point), {
        icon: dot(chosen ? 'selected' : point.estimated ? 'estimate' : 'fix', chosen ? 21 : 15),
        draggable: !adding && (!draft || chosen),
        title: (point.estimated ? 'Estimated point · ' : '') + when(point.time),
        zIndexOffset: chosen ? 500 : 0,
        autoPan: true,
      }).addTo(editLayer);
      marker.on('click', () => {
        if (adding) {
          if (!edge && next && next.stream === point.stream && point.time < next.time) onEdge({ left: point, right: next });
        } else onSelect(point);
      });
      marker.on('dragend', () => {
        const position = marker.getLatLng();
        onMove(point, position.lat, normalizeLon(position.lng));
      });
      if (adding && !edge && next?.breakBefore && next.stream === point.stream && point.time < next.time) {
        L.polyline([loc(point), loc(next)], { weight: 18, opacity: 0, bubblingMouseEvents: false })
          .on('click', () => onEdge({ left: point, right: next })).addTo(editLayer);
      }
    }
    for (const waypoint of waypoints) {
      L.marker(loc(waypoint), { icon: dot('waypoint', 18), interactive: false, zIndexOffset: 400 }).addTo(editLayer);
    }
  });
</script>

<div class="leaflet-host" class:placing={editing && adding && edge !== null} bind:this={node} aria-label="GPS route map"></div>
