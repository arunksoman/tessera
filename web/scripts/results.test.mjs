// Run with: npm test
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { beginStatement, resultCSV, resultJSON, resultJSONL } from '../src/lib/results.ts';

const fields = [
	{ name: 'id', kind: 'int' },
	{ name: 'note', kind: 'text' },
	{ name: 'raw', kind: 'bytes' },
	{ name: 'f', kind: 'float' }
];
const rows = [
	['-5', '=SUM(A1)', { b64: 'AAE=' }, 'NaN'],
	['12345678901234567890', 'a "quoted",\nline', null, '1.5e3']
];

test('resultCSV quotes, keeps numbers, and neutralizes formulas in text only', () => {
	assert.equal(
		resultCSV(fields, rows, true),
		'id,note,raw,f\r\n-5,\'=SUM(A1),AAE=,NaN\r\n12345678901234567890,"a ""quoted"",\nline",,1.5e3\r\n'
	);
	assert.match(resultCSV(fields, rows, false), /,=SUM\(A1\),/);
	assert.equal(resultCSV([{ name: '@x', kind: 'text' }], [], true), "'@x\r\n");
});

test('resultJSON keeps big integers exact and quotes what is not a JSON number', () => {
	const out = resultJSON(fields, rows);
	assert.match(out, /"id":12345678901234567890/);
	assert.match(out, /"f":"NaN"/);
	assert.match(out, /"raw":"AAE="/);
	assert.match(out, /"raw":null/);
	assert.deepEqual(JSON.parse(resultJSON(fields, [])), []);
	assert.equal(JSON.parse(out)[1].note, 'a "quoted",\nline');
});

test('resultJSONL writes one object per line', () => {
	const lines = resultJSONL(fields, rows).trimEnd().split('\n');
	assert.equal(lines.length, 2);
	assert.equal(JSON.parse(lines[0]).note, '=SUM(A1)');
});

test('beginStatement matches the dialect', () => {
	assert.equal(beginStatement('mysql'), 'START TRANSACTION');
	assert.equal(beginStatement('postgres'), 'BEGIN');
});
