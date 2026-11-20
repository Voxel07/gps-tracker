export type View = 'Day' | 'Month' | 'Year' | 'Custom';
export type Theme = 'dark' | 'light';
export type MapLayerId = 'osm' | 'light' | 'dark' | 'topo' | 'satellite';

export type MapLayerOption = {
  id: MapLayerId;
  label: string;
  shortLabel: string;
  url: string;
  attribution: string;
  maxZoom: number;
  subdomains?: string;
  isDark?: boolean;
};

export const mapLayers: MapLayerOption[] = [
  {
    id: 'osm',
    label: 'OpenStreetMap',
    shortLabel: 'Standard',
    url: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png',
    attribution: '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
    maxZoom: 19,
  },
  {
    id: 'light',
    label: 'CARTO Voyager',
    shortLabel: 'Light',
    url: 'https://{s}.basemaps.cartocdn.com/rastertiles/voyager/{z}/{x}/{y}{r}.png',
    attribution: '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> © <a href="https://carto.com/attributions">CARTO</a>',
    maxZoom: 20,
    subdomains: 'abcd',
  },
  {
    id: 'dark',
    label: 'CARTO Dark',
    shortLabel: 'Dark',
    url: 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png',
    attribution: '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> © <a href="https://carto.com/attributions">CARTO</a>',
    maxZoom: 20,
    subdomains: 'abcd',
    isDark: true,
  },
  {
    id: 'topo',
    label: 'OpenTopoMap',
    shortLabel: 'Topo',
    url: 'https://{s}.tile.opentopomap.org/{z}/{x}/{y}.png',
    attribution: '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · <a href="https://opentopomap.org">OpenTopoMap</a>',
    maxZoom: 17,
    subdomains: 'abc',
  },
  {
    id: 'satellite',
    label: 'Satellite',
    shortLabel: 'Satellite',
    url: 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}',
    attribution: 'Tiles © Esri — Source: Esri, Maxar, Earthstar Geographics',
    maxZoom: 19,
    isDark: true,
  },
];

export type Range = { from: number; to: number };
export type Point = {
  id: number;
  time: number;
  lat: number;
  lon: number;
  stream: string;
  revision: number;
  corrected: boolean;
  estimated: boolean;
  breakBefore: boolean;
};
export type Route = { points: Point[]; latest: Point | null; count: number; distance: number };
export type Photo = { id: string; time: number; lat: number; lon: number; estimated: boolean };
export type Key = { id: number; name: string; lastUsed: number | null; revoked: number | null };
export type Immich = { available: boolean; connected: boolean; url: string };
export type ImportSummary = { valid: number; invalid: number; duplicates: number; added: number; from: number | null; to: number | null };
export type Waypoint = { lat: number; lon: number; time: number };
export type Edge = { left: Point; right: Point };
export type RecentUpload = {
  id: number;
  deviceName: string;
  lat: number;
  lon: number;
  recordedAt: number;
  receivedAt: number;
  duplicate: boolean;
};
export type SessionInfo = {
  user: { name: string } | null;
  expiresAt: number | null;
  refreshable: boolean;
  signInAvailable: boolean;
};
export type ImportProgressState = {
  stage: 'uploading' | 'parsing' | 'checking' | 'saving';
  percent: number;
  bytesRead: number;
  totalBytes: number;
  processed: number;
  total: number;
  valid: number;
  invalid: number;
  added: number;
  duplicates: number;
};

export const emptyRoute: Route = { points: [], latest: null, count: 0, distance: 0 };

const THEME_KEY = 'gps-tracker-theme';
const LAYER_KEY = 'gps-tracker-map-layer';

export function getInitialTheme(): Theme {
  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (saved === 'dark' || saved === 'light') return saved;
  } catch { /* ignore storage errors */ }
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  const colorScheme = document.querySelector('meta[name="color-scheme"]');
  if (colorScheme) colorScheme.setAttribute('content', theme);
  const themeColor = document.querySelector('meta[name="theme-color"]');
  if (themeColor) themeColor.setAttribute('content', theme === 'dark' ? '#060517' : '#060517');
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch { /* ignore */ }
}

export function getInitialMapLayer(theme: Theme): MapLayerId {
  try {
    const saved = localStorage.getItem(LAYER_KEY) as MapLayerId | null;
    if (saved && mapLayers.some((layer) => layer.id === saved)) return saved;
  } catch { /* ignore */ }
  return theme === 'dark' ? 'dark' : 'osm';
}

export function saveMapLayer(id: MapLayerId) {
  try {
    localStorage.setItem(LAYER_KEY, id);
  } catch { /* ignore */ }
}

export function formatDistance(meters: number): string {
  if (!Number.isFinite(meters) || meters <= 0) return '0 m';
  if (meters < 1000) return Math.round(meters) + ' m';
  const km = meters / 1000;
  return (km >= 100 ? Math.round(km).toLocaleString() : km.toFixed(1)) + ' km';
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  if (bytes < 1024) return Math.round(bytes) + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(2) + ' MB';
}

export function formatCoords(lat: number, lon: number): string {
  return lat.toFixed(5) + ', ' + lon.toFixed(5);
}

export function calendarRange(view: Exclude<View, 'Custom'>, date: Date): Range {
  const year = date.getFullYear();
  const month = date.getMonth();
  const day = date.getDate();
  if (view === 'Year') return { from: +new Date(year, 0, 1), to: +new Date(year + 1, 0, 1) };
  if (view === 'Month') return { from: +new Date(year, month, 1), to: +new Date(year, month + 1, 1) };
  return { from: +new Date(year, month, day), to: +new Date(year, month, day + 1) };
}

export function adjacent(view: Exclude<View, 'Custom'>, date: Date, step: number): Date {
  const start = new Date(calendarRange(view, date).from);
  if (view === 'Year') start.setFullYear(start.getFullYear() + step);
  else if (view === 'Month') start.setMonth(start.getMonth() + step);
  else start.setDate(start.getDate() + step);
  return start;
}

export function heading(view: View, range: Range): string {
  const from = new Date(range.from);
  if (view === 'Day') return from.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' });
  if (view === 'Month') return from.toLocaleDateString(undefined, { month: 'long', year: 'numeric' });
  if (view === 'Year') return String(from.getFullYear());
  const format = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
  return format.formatRange(from, new Date(range.to - 1));
}

export function localDay(value: number) {
  const date = new Date(value);
  return [date.getFullYear(), String(date.getMonth() + 1).padStart(2, '0'), String(date.getDate()).padStart(2, '0')].join('-');
}

export function localDateTime(value: number) {
  const d = new Date(value);
  return localDay(value) + 'T' + [d.getHours(), d.getMinutes(), d.getSeconds()].map((part) => String(part).padStart(2, '0')).join(':');
}

export const when = (value: number) => new Date(value).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
export const whenShort = (value: number) => new Date(value).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' });
export const message = (error: unknown) => error instanceof Error ? error.message : 'Something went wrong.';
export const rangeQuery = (range: Range) => 'from=' + range.from + '&to=' + range.to;

type RefreshResult = { user: { name: string }; expiresAt: number } | null;
let refreshInFlight: Promise<RefreshResult> | null = null;
let sessionListener: ((data: RefreshResult) => void) | null = null;

export function onSessionUpdate(listener: (data: RefreshResult) => void) {
  sessionListener = listener;
}

export async function refreshSession(): Promise<RefreshResult> {
  if (refreshInFlight) return refreshInFlight;
  refreshInFlight = (async () => {
    try {
      const response = await fetch('/auth/refresh', { method: 'POST' });
      if (!response.ok) {
        sessionListener?.(null);
        return null;
      }
      const data = await response.json() as { user: { name: string }; expiresAt: number };
      sessionListener?.(data);
      return data;
    } catch {
      return null;
    } finally {
      refreshInFlight = null;
    }
  })();
  return refreshInFlight;
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const exec = () => fetch(path, options);
  let response = await exec();
  if (response.status === 401 && !path.startsWith('/auth/') && path !== '/api/session') {
    const refreshed = await refreshSession();
    if (refreshed) {
      response = await exec();
    }
  }
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    throw new Error(data.error ?? 'Request failed (' + response.status + ').');
  }
  return (response.status === 204 || response.status === 201 && response.headers.get('Content-Type') === null ? undefined : await response.json()) as T;
}

export function json(method: string, payload: unknown): RequestInit {
  return { method, body: JSON.stringify(payload), headers: { 'Content-Type': 'application/json' } };
}

export async function downloadGPX(range: Range): Promise<string> {
  const path = '/api/export?' + rangeQuery(range);
  let response = await fetch(path);
  if (response.status === 401) {
    const refreshed = await refreshSession();
    if (refreshed) {
      response = await fetch(path);
    }
  }
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    throw new Error(data.error ?? 'Export failed (' + response.status + ').');
  }
  const blob = await response.blob();
  const disposition = response.headers.get('Content-Disposition') ?? '';
  const match = /filename="([^"]+)"/.exec(disposition);
  const startDay = localDay(range.from);
  const endDay = localDay(range.to - 1);
  const fallback = startDay === endDay ? `gps-tracker-${startDay}.gpx` : `gps-tracker-${startDay}_to_${endDay}.gpx`;
  const filename = match?.[1] || fallback;
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  return filename;
}

export async function uploadImportWithProgress(
  file: File,
  format: 'gpx' | 'csv',
  preview: boolean,
  onProgress: (state: ImportProgressState) => void,
  retried = false,
): Promise<ImportSummary> {
  const url = '/api/import?format=' + format + '&preview=' + (preview ? '1' : '0') + '&stream=1';
  const result = await new Promise<{ status: number; summary?: ImportSummary; error?: string }>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    let offset = 0;
    let buffer = '';
    let finalSummary: ImportSummary | undefined;
    let streamError: string | undefined;

    const latest: ImportProgressState = {
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

    const emit = () => onProgress({ ...latest });

    const consumeLines = (chunk: string, flush = false) => {
      buffer += chunk;
      const lines = buffer.split('\n');
      buffer = flush ? '' : (lines.pop() ?? '');
      for (const raw of lines) {
        const line = raw.trim();
        if (!line) continue;
        try {
          const event = JSON.parse(line) as {
            type?: string;
            stage?: 'parsing' | 'checking' | 'saving';
            percent?: number;
            bytesRead?: number;
            totalBytes?: number;
            processed?: number;
            total?: number;
            valid?: number;
            invalid?: number;
            added?: number;
            duplicates?: number;
            summary?: ImportSummary;
            error?: string;
          };
          if (event.type === 'progress') {
            if (event.stage) latest.stage = event.stage;
            if (typeof event.percent === 'number') latest.percent = event.percent;
            if (typeof event.bytesRead === 'number') latest.bytesRead = event.bytesRead;
            if (typeof event.totalBytes === 'number' && event.totalBytes > 0) latest.totalBytes = event.totalBytes;
            if (typeof event.processed === 'number') latest.processed = event.processed;
            if (typeof event.total === 'number') latest.total = event.total;
            if (typeof event.valid === 'number') latest.valid = event.valid;
            if (typeof event.invalid === 'number') latest.invalid = event.invalid;
            if (typeof event.added === 'number') latest.added = event.added;
            if (typeof event.duplicates === 'number') latest.duplicates = event.duplicates;
            emit();
          } else if (event.type === 'done' && event.summary) {
            finalSummary = event.summary;
            latest.percent = 100;
            latest.valid = event.summary.valid;
            latest.invalid = event.summary.invalid;
            latest.added = event.summary.added;
            latest.duplicates = event.summary.duplicates;
            emit();
          } else if (event.type === 'error' && event.error) {
            streamError = event.error;
          }
        } catch {
          // Ignore partial/non-JSON lines until complete
        }
      }
    };

    xhr.upload.addEventListener('progress', (event) => {
      if (latest.stage !== 'uploading') return;
      latest.bytesRead = event.loaded;
      latest.totalBytes = event.lengthComputable ? event.total : file.size;
      latest.percent = latest.totalBytes > 0 ? Math.min(100, Math.round((event.loaded / latest.totalBytes) * 100)) : 0;
      emit();
    });

    xhr.upload.addEventListener('load', () => {
      if (latest.stage === 'uploading') {
        latest.stage = 'parsing';
        latest.percent = 0;
        latest.bytesRead = file.size;
        latest.totalBytes = file.size;
        emit();
      }
    });

    xhr.addEventListener('progress', () => {
      const text = xhr.responseText;
      if (text.length > offset) {
        consumeLines(text.slice(offset), false);
        offset = text.length;
      }
    });

    xhr.addEventListener('load', () => {
      const text = xhr.responseText;
      if (text.length > offset) {
        consumeLines(text.slice(offset), true);
        offset = text.length;
      } else if (buffer.trim()) {
        consumeLines('', true);
      }
      if (xhr.status === 401) {
        resolve({ status: 401 });
        return;
      }
      if (xhr.status < 200 || xhr.status >= 300) {
        try {
          const parsed = JSON.parse(text) as { error?: string };
          resolve({ status: xhr.status, error: parsed.error ?? 'Request failed (' + xhr.status + ').' });
        } catch {
          resolve({ status: xhr.status, error: 'Request failed (' + xhr.status + ').' });
        }
        return;
      }
      if (streamError) {
        resolve({ status: 400, error: streamError });
        return;
      }
      if (finalSummary) {
        resolve({ status: 200, summary: finalSummary });
        return;
      }
      resolve({ status: 500, error: 'Import finished without returning a summary.' });
    });

    xhr.addEventListener('error', () => reject(new TypeError('Network error while uploading file.')));
    xhr.addEventListener('abort', () => reject(new Error('Upload cancelled.')));

    emit();
    xhr.open('POST', url);
    xhr.send(file);
  });

  if (result.status === 401 && !retried) {
    const refreshed = await refreshSession();
    if (refreshed) {
      return uploadImportWithProgress(file, format, preview, onProgress, true);
    }
    throw new Error('Sign in to continue');
  }
  if (result.error || !result.summary) {
    throw new Error(result.error ?? 'Import failed.');
  }
  return result.summary;
}
