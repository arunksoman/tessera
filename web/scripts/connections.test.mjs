// Run with: npm test
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { envOf, filterProfiles, parseRecent, relativeTime, renameRecent, summary, touchRecent } from '../src/lib/connections.ts';

const profile = (name, values = {}, tile = 'postgres') => ({ name, tile, readOnly: false, noHistory: false, values, secrets: {} });

test('summary shows user, address, and database', () => {
	assert.equal(summary(profile('a', { host: 'db.local', port: '5432', user: 'app', database: 'shop' })), 'app@db.local:5432/shop');
	assert.equal(summary(profile('a', { host: 'db.local' })), 'db.local');
	assert.equal(summary(profile('a', { socket: '/run/mysqld.sock', database: 'x' })), '/run/mysqld.sock/x');
	assert.equal(summary(profile('a', {})), '');
});

test('envOf maps every environment to a colour and a text label', () => {
	assert.equal(envOf('production').color, 'production');
	assert.equal(envOf('production').label, 'Production');
	assert.equal(envOf('production').short, 'prod');
	assert.equal(envOf('development').short, 'dev');
	assert.equal(envOf('staging').short, 'staging');
	assert.equal(envOf('qa').short, 'qa');
	assert.equal(envOf('staging').color, 'staging');
	assert.equal(envOf('development').color, 'development');
	assert.equal(envOf(undefined).color, 'none');
	assert.equal(envOf('').label, 'None');
	// An environment this build does not know still shows its name, never a safe colour.
	assert.equal(envOf('qa').label, 'qa');
	assert.equal(envOf('qa').color, 'none');
});

test('touchRecent puts the latest first, once, and keeps ten', () => {
	let list = [];
	for (let i = 0; i < 12; i++) list = touchRecent(list, `c${i}`, 1000 + i);
	assert.equal(list.length, 10);
	assert.equal(list[0].name, 'c11');
	list = touchRecent(list, 'c5', 5000);
	assert.deepEqual(list.slice(0, 2).map((r) => r.name), ['c5', 'c11']);
	assert.equal(list.filter((r) => r.name === 'c5').length, 1);
	assert.equal(list[0].at, 5000);
});

test('renameRecent follows a renamed profile', () => {
	const list = renameRecent([{ name: 'old', at: 1 }, { name: 'new', at: 0 }], 'old', 'new');
	assert.deepEqual(list, [{ name: 'new', at: 1 }]);
});

test('parseRecent ignores anything malformed', () => {
	assert.deepEqual(parseRecent(null), []);
	assert.deepEqual(parseRecent('not json'), []);
	assert.deepEqual(parseRecent('{"a":1}'), []);
	assert.deepEqual(parseRecent('[{"name":"a","at":1},{"name":2},{"at":3},null]'), [{ name: 'a', at: 1 }]);
});

test('relativeTime reads like a person would say it', () => {
	const now = Date.UTC(2026, 10, 15, 12);
	assert.equal(relativeTime(now - 20_000, now), 'just now');
	assert.equal(relativeTime(now - 5 * 60_000, now), '5 minutes ago');
	assert.equal(relativeTime(now - 3 * 3600_000, now), '3 hours ago');
	assert.equal(relativeTime(now - 86400_000, now), 'yesterday');
	assert.equal(relativeTime(now - 90 * 86400_000, now), '3 months ago');
	assert.equal(relativeTime(now - 400 * 86400_000, now), 'last year');
});

test('filterProfiles matches name, database type, and address', () => {
	const ps = [profile('shop-prod', { host: 'db1' }), profile('blog', { host: 'mysql.local' }, 'mysql'), profile('crm', { host: 'x' })];
	assert.deepEqual(filterProfiles(ps, '').map((p) => p.name), ['shop-prod', 'blog', 'crm']);
	assert.deepEqual(filterProfiles(ps, 'SHOP').map((p) => p.name), ['shop-prod']);
	assert.deepEqual(filterProfiles(ps, 'mysql').map((p) => p.name), ['blog']);
	assert.deepEqual(filterProfiles(ps, 'db1').map((p) => p.name), ['shop-prod']);
});
