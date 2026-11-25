// Run with: npm test
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { baseName, crumbs, formatSize, isAbsolute, isDatabaseFile, join } from '../src/lib/paths.ts';

test('crumbs walk from the root, on both separators', () => {
	assert.deepEqual(crumbs('C:\\Users\\me', '\\'), [
		{ label: 'C:\\', path: 'C:\\' },
		{ label: 'Users', path: 'C:\\Users' },
		{ label: 'me', path: 'C:\\Users\\me' }
	]);
	assert.deepEqual(crumbs('/home/me/', '/'), [
		{ label: '/', path: '/' },
		{ label: 'home', path: '/home' },
		{ label: 'me', path: '/home/me' }
	]);
	assert.deepEqual(crumbs('/', '/'), [{ label: '/', path: '/' }]);
});

test('join adds one separator', () => {
	assert.equal(join('C:\\', 'a.db', '\\'), 'C:\\a.db');
	assert.equal(join('/home', 'a.db', '/'), '/home/a.db');
});

test('absolute paths', () => {
	for (const p of ['/x', 'C:\\x', 'c:/x', '\\\\srv\\share']) assert.ok(isAbsolute(p), p);
	for (const p of ['x.db', 'dir/x.db', 'C:x']) assert.ok(!isAbsolute(p), p);
});

test('names, database files, and sizes', () => {
	assert.equal(baseName('C:\\a\\b.db', '\\'), 'b.db');
	assert.ok(isDatabaseFile('App.SQLite3') && isDatabaseFile('x.db') && !isDatabaseFile('db.txt'));
	assert.equal(formatSize(5), '5 B');
	assert.equal(formatSize(1536), '1.5 KB');
	assert.equal(formatSize(5 * 1024 * 1024), '5.0 MB');
	assert.equal(formatSize(300 * 1024 * 1024), '300 MB');
});
