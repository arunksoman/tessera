// Run with: npm test
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { blankField, blankIndex, changes, draftOf, pendingCount, rowState, splitList, targetLabel } from '../src/lib/design.ts';

const field = (name, extra = {}) => ({ ...blankField(), name, type: 'int', nullable: false, ...extra });
const structure = () => ({
	fields: [field('id', { primary: true }), field('name', { type: 'text', nullable: true })],
	indexes: [{ ...blankIndex(), name: 'pk', fields: ['id'], unique: true, primary: true, constraint: true, definition: 'x' }],
	relations: [
		{ name: 'fk', fields: ['owner'], target: [{ kind: 'schema', name: 's' }, { kind: 'table', name: 'u' }], targetFields: ['id'], onUpdate: '', onDelete: '' }
	],
	triggers: [{ name: 'trg', timing: 'BEFORE', events: ['INSERT'], forEach: 'ROW', condition: '', action: 'EXECUTE FUNCTION f()', definition: 'd' }],
	actions: []
});

test('an untouched draft changes nothing', () => {
	const d = draftOf(structure());
	assert.equal(pendingCount(d), 0);
	assert.deepEqual(changes(d), []);
});

test('editing does not touch the described structure', () => {
	const s = structure();
	const d = draftOf(s);
	d.fields[1].cur.name = 'label';
	assert.equal(s.fields[1].name, 'name');
	assert.equal(rowState('fields', d.fields[1]), 'changed');
});

test('changes run in dependency order', () => {
	const d = draftOf(structure());
	d.triggers[0].cur.action = 'EXECUTE FUNCTION g()';
	d.relations[0].deleted = true;
	d.indexes[0].deleted = true;
	d.fields[1].cur.comment = 'who';
	d.fields[0].deleted = true;
	d.fields.push({ orig: null, cur: field('qty'), deleted: false });
	d.indexes.push({ orig: null, cur: { ...blankIndex(), name: 'q', fields: ['qty'] }, deleted: false });
	const ops = changes(d).map((c) => c.op + ' ' + (c.name ?? c.field?.name ?? c.index?.name ?? c.trigger?.name ?? ''));
	assert.deepEqual(ops, [
		'trigger.drop trg',
		'relation.drop fk',
		'index.drop pk',
		'field.drop id',
		'field.alter name',
		'field.add qty',
		'index.add q',
		'trigger.add trg'
	]);
	const alter = changes(d).find((c) => c.op === 'field.alter');
	assert.equal(alter.from.comment, '');
	assert.equal(alter.field.comment, 'who');
	// A dropped index carries how it was described, so a constraint is dropped as one.
	assert.equal(changes(d).find((c) => c.op === 'index.drop').index.constraint, true);
	assert.equal(pendingCount(d), 7);
});

test('read-only text does not count as a change', () => {
	const d = draftOf(structure());
	d.indexes[0].cur.definition = 'other';
	d.triggers[0].cur.definition = 'other';
	assert.equal(pendingCount(d), 0);
});

test('a row added and deleted here changes nothing', () => {
	const d = draftOf(structure());
	d.fields.push({ orig: null, cur: field('tmp'), deleted: true });
	assert.equal(pendingCount(d), 0);
	assert.deepEqual(changes(d), []);
});

test('splitList keeps expressions whole', () => {
	assert.deepEqual(splitList(' a, b ,,(lower(c), d)'), ['a', 'b', '(lower(c), d)']);
	assert.deepEqual(splitList(''), []);
});

test('targetLabel names other schemas', () => {
	const ref = [{ kind: 'schema', name: 's' }, { kind: 'table', name: 'u' }];
	assert.equal(targetLabel(ref, 's'), 'u');
	assert.equal(targetLabel(ref, 'other'), 's.u');
	assert.equal(targetLabel([], 's'), '');
});

test('draftOf copies objects that structuredClone refuses, such as proxies', () => {
	const s = structure();
	const proxied = new Proxy(s, {});
	proxied.fields = s.fields.map((f) => new Proxy(f, {}));
	const d = draftOf(proxied);
	d.fields[0].cur.name = 'x';
	assert.equal(d.fields[0].orig.name, 'id');
});
