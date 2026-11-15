// Pure helpers for the connection list, start page, and form. No Svelte state here, so
// scripts/connections.test.mjs can run them under plain Node.
import type { Profile } from './api.ts';

/**
 * The colours a user can mark a connection with in the web app. Only the name is stored;
 * internal/server/api.go accepts the same list. A colour is a personal reminder, not an
 * environment: safety rules read the profile's environment tag, which the TUI shows.
 */
export const colors = [
	{ name: '', label: 'No colour' },
	{ name: 'red', label: 'Red' },
	{ name: 'orange', label: 'Orange' },
	{ name: 'yellow', label: 'Yellow' },
	{ name: 'green', label: 'Green' },
	{ name: 'cyan', label: 'Cyan' },
	{ name: 'blue', label: 'Blue' },
	{ name: 'purple', label: 'Purple' },
	{ name: 'pink', label: 'Pink' }
] as const;

/** The CSS class suffix for a stored colour: `conn-<name>`, or `conn-none` for anything else. */
export function colorOf(name: string | undefined): string {
	return name && colors.some((c) => c.name === name) ? name : 'none';
}

/** A one-line address such as `app@db.local:5432/shop`, built from the usual field names. */
export function summary(p: Profile): string {
	const v = p.values;
	let where = v.host ? v.host + (v.port ? `:${v.port}` : '') : (v.socket ?? '');
	if (v.database) where += `/${v.database}`;
	if (!where) return '';
	return v.user && v.host ? `${v.user}@${where}` : where;
}

export function filterProfiles(profiles: Profile[], query: string): Profile[] {
	const q = query.trim().toLowerCase();
	if (!q) return profiles;
	return profiles.filter((p) => [p.name, p.tile, summary(p)].some((s) => s.toLowerCase().includes(q)));
}

export type Recent = { name: string; at: number };
const recentMax = 10;

/** Moves a connection to the front of the recent list, stamped with `at` in milliseconds. */
export function touchRecent(list: Recent[], name: string, at: number): Recent[] {
	return [{ name, at }, ...list.filter((r) => r.name !== name)].slice(0, recentMax);
}

export function renameRecent(list: Recent[], from: string, to: string): Recent[] {
	const out: Recent[] = [];
	for (const r of list) {
		const name = r.name === from ? to : r.name;
		if (!out.some((o) => o.name === name)) out.push({ name, at: r.at });
	}
	return out;
}

/** Reads the stored list, dropping anything that is not a well-formed entry. */
export function parseRecent(text: string | null): Recent[] {
	if (!text) return [];
	let raw: unknown;
	try {
		raw = JSON.parse(text);
	} catch {
		return [];
	}
	if (!Array.isArray(raw)) return [];
	return raw
		.filter((r): r is Recent => !!r && typeof r.name === 'string' && typeof r.at === 'number')
		.map((r) => ({ name: r.name, at: r.at }))
		.slice(0, recentMax);
}

const units: [Intl.RelativeTimeFormatUnit, number][] = [
	['year', 365 * 86400],
	['month', 30 * 86400],
	['week', 7 * 86400],
	['day', 86400],
	['hour', 3600],
	['minute', 60]
];
const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

export function relativeTime(at: number, now = Date.now()): string {
	const secs = Math.max(0, (now - at) / 1000);
	for (const [unit, size] of units) {
		if (secs >= size) return rtf.format(-Math.floor(secs / size), unit);
	}
	return 'just now';
}
