// Run with: npm test
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { tidy } from '../src/lib/menu.ts';

test('tidy drops leading, trailing, and repeated separators', () => {
	const a = { label: 'a', action() {} };
	const b = { label: 'b', action() {} };
	assert.deepEqual(tidy(['separator', a, 'separator', 'separator', b, 'separator']), [a, 'separator', b]);
	assert.deepEqual(tidy(['separator']), []);
});
