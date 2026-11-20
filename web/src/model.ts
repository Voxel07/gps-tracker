export type View = 'Day' | 'Month' | 'Year' | 'Custom';
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
export type Route = { points: Point[]; latest: Point | null; count: number };
export type Photo = { id: string; time: number; lat: number; lon: number; estimated: boolean };
export type Key = { id: number; name: string; lastUsed: number | null; revoked: number | null };
export type Immich = { available: boolean; connected: boolean; url: string };
export type ImportSummary = { valid: number; invalid: number; duplicates: number; added: number; from: number | null; to: number | null };
export type Waypoint = { lat: number; lon: number; time: number };
export type Edge = { left: Point; right: Point };

export const emptyRoute: Route = { points: [], latest: null, count: 0 };

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
export const message = (error: unknown) => error instanceof Error ? error.message : 'Something went wrong.';
export const rangeQuery = (range: Range) => 'from=' + range.from + '&to=' + range.to;

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, options);
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    throw new Error(data.error ?? 'Request failed (' + response.status + ').');
  }
  return (response.status === 204 || response.status === 201 && response.headers.get('Content-Type') === null ? undefined : await response.json()) as T;
}

export function json(method: string, payload: unknown): RequestInit {
  return { method, body: JSON.stringify(payload), headers: { 'Content-Type': 'application/json' } };
}
