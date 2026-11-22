// The structure editor's model: rows as described, edits on top, and the changes they make.
// Pure functions, tested by scripts/design.test.mjs.
import type { FieldDef, IndexDef, RelationDef, Structure, StructureChange, TriggerDef } from './api.ts';

/** One editable row. orig is null for a row added here; deleted marks one to drop. */
export type Row<T> = { orig: T | null; cur: T; deleted: boolean };

export type Draft = {
	fields: Row<FieldDef>[];
	indexes: Row<IndexDef>[];
	relations: Row<RelationDef>[];
	triggers: Row<TriggerDef>[];
};

export type Section = keyof Draft;

// A JSON copy, since structuredClone refuses the proxies Svelte wraps state in.
const copy = <T>(x: T): T => JSON.parse(JSON.stringify(x));
const rows = <T>(list: T[]): Row<T>[] => list.map((x) => ({ orig: copy(x), cur: copy(x), deleted: false }));

export function draftOf(s: Structure): Draft {
	return { fields: rows(s.fields), indexes: rows(s.indexes), relations: rows(s.relations), triggers: rows(s.triggers) };
}

export const blankField = (): FieldDef => ({
	name: '',
	type: '',
	nullable: true,
	default: '',
	comment: '',
	primary: false,
	collation: '',
	extra: ''
});
export const blankIndex = (): IndexDef => ({
	name: '',
	fields: [],
	unique: false,
	primary: false,
	constraint: false,
	method: '',
	definition: ''
});
export const blankRelation = (): RelationDef => ({ name: '', fields: [], target: [], targetFields: [], onUpdate: '', onDelete: '' });
export const blankTrigger = (): TriggerDef => ({
	name: '',
	timing: 'BEFORE',
	events: ['INSERT'],
	forEach: 'ROW',
	condition: '',
	action: '',
	definition: ''
});

/** The parts of each kind of row that a change sends; read-only parts, such as the database's own definition text, are left out. */
const editable: Record<Section, (x: never) => unknown> = {
	fields: (f: FieldDef) => f,
	indexes: (i: IndexDef) => [i.name, i.fields, i.unique, i.primary, i.method],
	relations: (r: RelationDef) => [r.name, r.fields, r.target, r.targetFields, r.onUpdate, r.onDelete],
	triggers: (t: TriggerDef) => [t.name, t.timing, t.events, t.forEach, t.condition, t.action]
};

function same<T>(section: Section, a: T, b: T): boolean {
	const key = editable[section] as (x: T) => unknown;
	return JSON.stringify(key(a)) === JSON.stringify(key(b));
}

export type RowState = 'same' | 'new' | 'changed' | 'deleted';

export function rowState<T>(section: Section, r: Row<T>): RowState {
	if (r.deleted) return 'deleted';
	if (!r.orig) return 'new';
	return same(section, r.orig, r.cur) ? 'same' : 'changed';
}

/** Rows that will change something. A row added and then deleted here changes nothing. */
export function pendingCount(d: Draft): number {
	let n = 0;
	for (const section of Object.keys(d) as Section[]) {
		for (const r of d[section] as Row<unknown>[]) {
			if (r.orig || !r.deleted) n += rowState(section, r) === 'same' ? 0 : 1;
		}
	}
	return n;
}

/**
 * The changes a draft makes, ordered so each can run: drops of what depends on fields first,
 * then fields, then what is added on top of them. A changed index, relation, or trigger is
 * dropped and added again.
 */
export function changes(d: Draft): StructureChange[] {
	const out: StructureChange[] = [];
	const drops = <T>(section: Section, make: (orig: T) => StructureChange) => {
		for (const r of d[section] as Row<T>[]) {
			const st = rowState(section, r);
			if (r.orig && (st === 'deleted' || (st === 'changed' && section !== 'fields'))) out.push(make(r.orig));
		}
	};
	const adds = <T>(section: Section, make: (cur: T) => StructureChange) => {
		for (const r of d[section] as Row<T>[]) {
			const st = rowState(section, r);
			if (st === 'new' || (st === 'changed' && section !== 'fields')) out.push(make(r.cur));
		}
	};
	drops<TriggerDef>('triggers', (t) => ({ op: 'trigger.drop', name: t.name }));
	drops<RelationDef>('relations', (r) => ({ op: 'relation.drop', name: r.name }));
	drops<IndexDef>('indexes', (i) => ({ op: 'index.drop', index: i }));
	drops<FieldDef>('fields', (f) => ({ op: 'field.drop', name: f.name }));
	for (const r of d.fields) {
		if (r.orig && rowState('fields', r) === 'changed') out.push({ op: 'field.alter', from: r.orig, field: r.cur });
	}
	adds<FieldDef>('fields', (f) => ({ op: 'field.add', field: f }));
	adds<IndexDef>('indexes', (i) => ({ op: 'index.add', index: i }));
	adds<RelationDef>('relations', (r) => ({ op: 'relation.add', relation: r }));
	adds<TriggerDef>('triggers', (t) => ({ op: 'trigger.add', trigger: t }));
	return out;
}

/** Splits a comma-separated list of names, keeping an expression in parentheses whole. */
export function splitList(s: string): string[] {
	const out: string[] = [];
	let depth = 0;
	let cur = '';
	for (const ch of s) {
		if (ch === '(') depth++;
		if (ch === ')') depth = Math.max(0, depth - 1);
		if (ch === ',' && depth === 0) {
			out.push(cur);
			cur = '';
			continue;
		}
		cur += ch;
	}
	out.push(cur);
	return out.map((x) => x.trim()).filter((x) => x !== '');
}

export const joinList = (a: string[]) => a.join(', ');

/** A target ref's last name, or schema.name when it is in another schema. */
export function targetLabel(target: { name: string }[], schema: string): string {
	if (target.length === 0) return '';
	const name = target[target.length - 1].name;
	const parent = target.length > 1 ? target[target.length - 2].name : '';
	return parent && parent !== schema ? `${parent}.${name}` : name;
}
