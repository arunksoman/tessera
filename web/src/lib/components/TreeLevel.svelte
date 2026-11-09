<script lang="ts">
	import { api, type Ref, type TreeNode } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import Self from './TreeLevel.svelte';

	// One level of the object tree, loaded a page at a time.
	let { profile, parent, depth }: { profile: string; parent: Ref; depth: number } = $props();

	let nodes = $state<TreeNode[]>([]);
	let next = $state('');
	let loading = $state(false);
	let error = $state('');
	let open = $state<Record<string, boolean>>({});

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
	const icon: Record<string, string> = { database: '◫', schema: '▤', table: '▦', view: '◇', 'materialized view': '◆' };

	function activate(n: TreeNode) {
		if (n.browsable) app.openBrowse(profile, n.ref);
		else if (n.hasChildren) open[key(n)] = !open[key(n)];
	}
</script>

<ul role={depth === 0 ? 'tree' : 'group'}>
	{#each nodes as n (key(n))}
		<li role="treeitem" aria-expanded={n.hasChildren ? !!open[key(n)] : undefined} aria-selected="false">
			<div class="row" style:--depth={depth}>
				{#if n.hasChildren}
					<button class="twisty" aria-label={open[key(n)] ? 'Collapse' : 'Expand'} onclick={() => (open[key(n)] = !open[key(n)])}>
						{open[key(n)] ? '▾' : '▸'}
					</button>
				{:else}
					<span class="twisty"></span>
				{/if}
				<button class="label" title="{n.kind}: {n.label}" onclick={() => activate(n)}>
					<span class="icon" aria-hidden="true">{icon[n.kind] ?? '•'}</span>
					<span class="text">{n.label}</span>
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
		color: var(--muted);
		width: 14px;
		text-align: center;
	}
	.text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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
