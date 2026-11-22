<script lang="ts">
	import { api, type Ref, type TreeNode } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import type { IconName } from '#lib/icons/index.ts';
	import { copyText, type MenuAt, type MenuItem } from '#lib/menu.ts';
	import ContextMenu from './ContextMenu.svelte';
	import Icon from './Icon.svelte';
	import Self from './TreeLevel.svelte';

	// One level of the object tree, loaded a page at a time.
	let { profile, parent, depth }: { profile: string; parent: Ref; depth: number } = $props();

	let nodes = $state<TreeNode[]>([]);
	let next = $state('');
	let loading = $state(false);
	let error = $state('');
	let open = $state<Record<string, boolean>>({});
	let menuAt = $state<MenuAt | null>(null);

	async function load(cursor = '') {
		loading = true;
		error = '';
		try {
			const page = await api.tree(profile, parent, cursor);
			nodes = cursor ? [...nodes, ...page.nodes] : page.nodes;
			next = page.next;
		} catch (e) {
			error = (e as Error).message;
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		void app.treeVersion[profile];
		load();
	});

	const key = (n: TreeNode) => JSON.stringify(n.ref);
	const icon: Record<string, IconName> = {
		database: 'database',
		schema: 'schema',
		table: 'table',
		view: 'view',
		'materialized view': 'matview',
		function: 'fn',
		procedure: 'fn',
		aggregate: 'fn'
	};
	// Leaves such as parameters show no bullet: their detail says what they are.
	const plain = new Set(['parameter', 'return']);

	function activate(n: TreeNode) {
		if (n.browsable) app.openBrowse(profile, n.ref);
		else if (n.hasChildren) open[key(n)] = !open[key(n)];
	}

	async function showCreate(n: TreeNode) {
		try {
			const { script } = await api.createScript(profile, n.ref);
			app.openSQL(profile, script);
		} catch (e) {
			app.notify(`Could not read the definition of ${n.label}: ${(e as Error).message}`, 'error');
		}
	}

	function items(n: TreeNode): MenuItem[] {
		const out: MenuItem[] = [];
		const readOnly = !!app.profile(profile)?.readOnly;
		const dialog = (kind: 'export' | 'import' | 'rename' | 'truncate' | 'drop') => () => (app.dialog = { kind, profile, ref: n.ref });
		if (n.browsable) out.push({ label: 'Open data', icon: 'table', action: () => app.openBrowse(profile, n.ref) });
		if (n.hasChildren) out.push({ label: open[key(n)] ? 'Collapse' : 'Expand', action: () => (open[key(n)] = !open[key(n)]) });
		// Tables and views can be designed, exported, and managed; tables also take data.
		const table = n.browsable && n.kind === 'table';
		if (table || (n.browsable && n.kind === 'view')) {
			out.push(
				{ label: table && !readOnly ? 'Structure…' : 'View structure', icon: 'wrench', action: () => app.openStructure(profile, n.ref) },
				{ label: 'SQL: Create', icon: 'query', action: () => showCreate(n) },
				'separator',
				{ label: 'Export…', icon: 'download', action: dialog('export') }
			);
			if (table) out.push({ label: 'Import data…', icon: 'upload', disabled: readOnly, action: dialog('import') });
			out.push('separator', { label: 'Rename…', icon: 'edit', disabled: readOnly, action: dialog('rename') });
			if (table) out.push({ label: 'Truncate…', icon: 'eraser', danger: true, disabled: readOnly, action: dialog('truncate') });
			out.push({ label: `Drop ${n.kind}…`, icon: 'trash', danger: true, disabled: readOnly, action: dialog('drop') });
		}
		out.push('separator', { label: 'Copy name', icon: 'copy', action: () => copyText(n.label) });
		// A routine's ref names its signature, which tells overloads apart.
		const refName = n.ref[n.ref.length - 1]?.name ?? '';
		if (refName !== n.label) out.push({ label: 'Copy signature', action: () => copyText(refName) });
		if (n.detail) out.push({ label: 'Copy type', action: () => copyText(n.detail ?? '') });
		out.push('separator', { label: 'Refresh', action: () => app.refreshTree(profile) });
		return out;
	}

	function contextMenu(e: MouseEvent, n: TreeNode) {
		e.preventDefault();
		menuAt = { x: e.clientX, y: e.clientY, items: items(n) };
	}

	function menuKey(e: KeyboardEvent, n: TreeNode) {
		if (e.key !== 'ContextMenu' && !(e.shiftKey && e.key === 'F10')) return;
		e.preventDefault();
		const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
		menuAt = { x: r.left + 16, y: r.bottom, items: items(n) };
	}
</script>

<ul role={depth === 0 ? 'tree' : 'group'}>
	{#each nodes as n (key(n))}
		<li role="treeitem" aria-expanded={n.hasChildren ? !!open[key(n)] : undefined} aria-selected="false">
			<div class="row" style:--depth={depth}>
				{#if n.hasChildren}
					<button class="twisty" aria-label={open[key(n)] ? 'Collapse' : 'Expand'} onclick={() => (open[key(n)] = !open[key(n)])}>
						<Icon name={open[key(n)] ? 'chevronDown' : 'chevronRight'} size={14} />
					</button>
				{:else}
					<span class="twisty"></span>
				{/if}
				<button
					class="label"
					title="{n.kind}: {n.ref[n.ref.length - 1]?.name ?? n.label}{n.detail ? ` ${n.detail}` : ''}"
					onclick={() => activate(n)}
					oncontextmenu={(e) => contextMenu(e, n)}
					onkeydown={(e) => menuKey(e, n)}
				>
					<span class="icon" class:fn={icon[n.kind] === 'fn'} aria-hidden="true"
						>{#if icon[n.kind]}<Icon name={icon[n.kind]} size={14} />{:else if !plain.has(n.kind)}•{/if}</span
					>
					<span class="text">{n.label}</span>
					{#if n.detail}<span class="detail">{n.detail}</span>{/if}
				</button>
			</div>
			{#if open[key(n)]}<Self {profile} parent={n.ref} depth={depth + 1} />{/if}
		</li>
	{/each}
	{#if loading}<li class="note" style:--depth={depth}>Loading…</li>{/if}
	{#if error}<li class="note err" style:--depth={depth}>{error}</li>{/if}
	{#if !loading && !error && nodes.length === 0}<li class="note" style:--depth={depth}>Empty</li>{/if}
	{#if next && !loading}
		<li style:--depth={depth}><button class="more" onclick={() => load(next)}>Load more…</button></li>
	{/if}
</ul>

{#if menuAt}
	<ContextMenu x={menuAt.x} y={menuAt.y} items={menuAt.items} label="Object actions" onclose={() => (menuAt = null)} />
{/if}

<style>
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.row {
		display: flex;
		align-items: center;
		padding-left: calc(var(--depth) * 14px + 8px);
		height: 26px;
	}
	.row:hover {
		background: var(--hover);
	}
	.twisty {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 16px;
		flex: none;
		border: 0;
		background: none;
		color: var(--muted);
		cursor: pointer;
		padding: 0;
		font-size: 11px;
	}
	.label {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: 1;
		min-width: 0;
		height: 100%;
		border: 0;
		background: none;
		cursor: pointer;
		text-align: left;
		padding: 0 4px;
	}
	.icon {
		display: inline-flex;
		justify-content: center;
		flex: none;
		color: var(--muted);
		width: 14px;
	}
	.icon.fn {
		color: var(--porphyry);
	}
	.text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.detail {
		margin-left: auto;
		padding-left: 8px;
		flex: 0 1 auto;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--muted);
		font-family: var(--font-mono);
		font-size: 11px;
	}
	.note,
	.more {
		padding-left: calc(var(--depth) * 14px + 30px);
		color: var(--muted);
		font-size: 12px;
		height: 24px;
		display: flex;
		align-items: center;
	}
	.err {
		color: var(--danger);
		height: auto;
		padding-top: 4px;
		padding-bottom: 4px;
		white-space: normal;
	}
	.more {
		border: 0;
		background: none;
		cursor: pointer;
		color: var(--lapis);
	}
</style>
