// Run with: npm test
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { browseTitle, columnMarks, followFilters, rowJSON, rowTSV, withLayoutTypes } from '../src/lib/keys.ts';

const fk = (fields, targetFields, name = 'fk') => ({
	name,
	fields,
	target: [{ kind: 'schema', name: 'public' }, { kind: 'table', name: 'customers' }],
	targetFields
});

test('columnMarks flags primary and foreign key columns', () => {
	const marks = columnMarks({ primary: ['id'], foreign: [fk(['customer_id'], ['id']), fk(['id'], ['id'], 'self')] });
	assert.equal(marks.id.pk, true);
	// A column in both keys keeps both marks, and the first reference wins.
	assert.equal(marks.id.fk.name, 'self');
	assert.equal(marks.customer_id.pk, false);
	assert.equal(marks.customer_id.fk.targetFields[0], 'id');
	assert.equal(marks.total, undefined);
	assert.deepEqual(columnMarks(undefined), {});
});

test('followFilters matches every column of the key', () => {
	const names = ['id', 'a', 'b'];
	assert.deepEqual(followFilters(fk(['b', 'a'], ['x', 'y']), names, ['1', 'A', 'B']), [
		{ field: 'x', op: '=', value: 'B' },
		{ field: 'y', op: '=', value: 'A' }
	]);
	// NULL references nothing, and bytes or missing columns cannot be matched as text.
	assert.equal(followFilters(fk(['a'], ['x']), names, ['1', null, 'B']), null);
	assert.equal(followFilters(fk(['a'], ['x']), names, ['1', { b64: 'AA==' }, 'B']), null);
	assert.equal(followFilters(fk(['zz'], ['x']), names, ['1', 'A', 'B']), null);
});

test('withLayoutTypes prefers the declared column type', () => {
	const got = withLayoutTypes(
		[
			{ name: 'rating', type: 'oid:16390', kind: 'other' },
			{ name: 'extra', type: 'int4', kind: 'int' }
		],
		[{ name: 'rating', type: 'mpaa_rating', kind: 'null' }]
	);
	assert.deepEqual(got, [
		{ name: 'rating', type: 'mpaa_rating', kind: 'other' },
		{ name: 'extra', type: 'int4', kind: 'int' }
	]);
});

test('rows copy as tab-separated text and as JSON', () => {
	const names = ['id', 'note', 'raw'];
	const row = ['1', 'a\tb\nc', null];
	assert.equal(rowTSV(row), '1\ta b c\t');
	assert.equal(rowJSON(names, row), '{"id":"1","note":"a\\tb\\nc","raw":null}');
});

test('browseTitle names the target and its filter', () => {
	const ref = [{ kind: 'schema', name: 'public' }, { kind: 'table', name: 'customers' }];
	assert.equal(browseTitle(ref), 'customers');
	assert.equal(browseTitle(ref, [{ field: 'id', op: '=', value: '7' }]), 'customers (id = 7)');
	const long = 'x'.repeat(80);
	assert.ok(browseTitle(ref, [{ field: 'id', op: '=', value: long }]).length < 60);
});
