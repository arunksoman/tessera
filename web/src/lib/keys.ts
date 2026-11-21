// Keys, foreign-key jumps, and copy formats for the grid. Pure functions, tested by
// scripts/keys.test.mjs.
import type { Cell, Field, Filter, ForeignKey, Keys, Ref } from './api.ts';

export type Mark = { pk: boolean; fk: ForeignKey | null };

/** Marks each column of the primary key and of a foreign key. The first reference wins. */
export function columnMarks(keys: Keys | undefined | null): Record<string, Mark> {
	const out: Record<string, Mark> = {};
	const mark = (name: string) => (out[name] ??= { pk: false, fk: null });
	for (const name of keys?.primary ?? []) mark(name).pk = true;
	for (const fk of keys?.foreign ?? []) {
		for (const name of fk.fields) {
			const m = mark(name);
			m.fk ??= fk;
		}
	}
	return out;
}

/**
 * Filters that find the record a row refers to, or null when it refers to none: a NULL in
 * the key, a column not in the result, or bytes that cannot be matched as text.
 */
export function followFilters(fk: ForeignKey, names: string[], row: Cell[]): Filter[] | null {
	const out: Filter[] = [];
	for (let i = 0; i < fk.fields.length; i++) {
		const c = names.indexOf(fk.fields[i]);
		const v = c < 0 ? null : row[c];
		if (typeof v !== 'string') return null;
		out.push({ field: fk.targetFields[i], op: '=', value: v });
	}
	return out;
}

/**
 * The declared type of each column from the layout, such as numeric(4,2) or an enum's name,
 * in place of the type a result reports.
 */
export function withLayoutTypes(fields: Field[], layout: Field[]): Field[] {
	const declared = new Map(layout.filter((f) => f.type).map((f) => [f.name, f.type]));
	return fields.map((f) => (declared.has(f.name) ? { ...f, type: declared.get(f.name) } : f));
}

const flat = (c: Cell) => (c === null ? '' : typeof c === 'string' ? c : `\\x${c.b64}`).replace(/[\t\r\n]+/g, ' ');

/** One row as tab-separated text, for pasting into a spreadsheet. NULL is empty. */
export function rowTSV(row: Cell[]): string {
	return row.map(flat).join('\t');
}

/** One row as a JSON object. Values stay strings, so numbers keep every digit. */
export function rowJSON(names: string[], row: Cell[]): string {
	const o: Record<string, Cell> = {};
	names.forEach((n, i) => (o[n] = row[i]));
	return JSON.stringify(o);
}

/** A browse tab's title: the object's name, and the filter it was opened with. */
export function browseTitle(ref: Ref, filter: Filter[] = []): string {
	const name = ref[ref.length - 1]?.name ?? '';
	if (!filter.length) return name;
	let where = filter.map((f) => `${f.field} ${f.op} ${typeof f.value === 'string' ? f.value : 'NULL'}`).join(', ');
	if (where.length > 40) where = where.slice(0, 39) + '…';
	return `${name} (${where})`;
}
