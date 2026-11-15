// Pure helpers for the connection list, start page, and form. No Svelte state here, so
// scripts/connections.test.mjs can run them under plain Node.
import type { Profile } from './api.ts';

export type EnvColor = 'none' | 'development' | 'staging' | 'production';
/** `short` is for narrow places such as the sidebar. */
export type Env = { value: string; label: string; short: string; color: EnvColor; note: string };

/**
 * Environments in the order the form offers them. The colour is what the web app shows; the
 * value is what the engine's safety rules read, so production still confirms every write.
 */
export const envs: Env[] = [
	{ value: '', label: 'None', short: '', color: 'none', note: 'No environment tag' },
	{ value: 'development', label: 'Development', short: 'dev', color: 'development', note: 'Local or personal databases' },
	{ value: 'staging', label: 'Staging', short: 'staging', color: 'staging', note: 'Shared test systems' },
	{ value: 'production', label: 'Production', short: 'prod', color: 'production', note: 'Every write asks you to type the connection name' }
];

/** The colour and label for a profile's environment. An unknown tag keeps its own name. */
export function envOf(value: string | undefined): Env {
	const v = value ?? '';
	return envs.find((e) => e.value === v) ?? { value: v, label: v, short: v, color: 'none', note: '' };
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
