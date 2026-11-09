<script lang="ts">
	import { api, type Profile } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import TileMark from './TileMark.svelte';
	import TreeLevel from './TreeLevel.svelte';

	let { onedit }: { onedit: (p?: Profile) => void } = $props();

	let expanded = $state<Record<string, boolean>>({});
	let deleting = $state<string | null>(null);

	async function remove(name: string) {
		try {
			app.closeProfileTabs(name);
			await api.deleteProfile(name);
			await app.reloadProfiles();
			app.notify(`Deleted ${name}`);
		} catch (e) {
			app.notify((e as Error).message, 'error');
		} finally {
			deleting = null;
		}
	}
</script>

<nav aria-label="Connections">
	<div class="head">
		<h2>Connections</h2>
		<button class="btn ghost icon" title="New connection" aria-label="New connection" onclick={() => onedit()}>＋</button>
	</div>
	<div class="list">
		{#each app.profiles as p (p.name)}
			<section>
				<div class="conn" class:open={expanded[p.name]}>
					<button class="name" aria-expanded={!!expanded[p.name]} onclick={() => (expanded[p.name] = !expanded[p.name])}>
						<span class="twisty" aria-hidden="true">{expanded[p.name] ? '▾' : '▸'}</span>
						<TileMark tile={p.tile} />
						<span class="text">{p.name}</span>
						{#if p.env}<span class="badge {p.env}">{p.env === 'production' ? 'prod' : p.env.slice(0, 5)}</span>{/if}
						{#if p.readOnly}<span class="badge ro" title="Read-only">ro</span>{/if}
					</button>
					<span class="actions">
						<button class="btn ghost icon" title="New SQL tab" aria-label="New SQL tab for {p.name}" onclick={() => app.openSQL(p.name)}>⌨</button>
						<button class="btn ghost icon" title="Edit" aria-label="Edit {p.name}" onclick={() => onedit(p)}>✎</button>
						<button class="btn ghost icon" title="Delete" aria-label="Delete {p.name}" onclick={() => (deleting = p.name)}>🗑</button>
					</span>
				</div>
				{#if deleting === p.name}
					<div class="confirm" role="alert">
						Delete <strong>{p.name}</strong> and its saved password?
						<span>
							<button class="btn danger" onclick={() => remove(p.name)}>Delete</button>
							<button class="btn" onclick={() => (deleting = null)}>Keep</button>
						</span>
					</div>
				{/if}
				{#if expanded[p.name]}<TreeLevel profile={p.name} parent={[]} depth={1} />{/if}
			</section>
		{:else}
			<p class="empty">No connections yet.</p>
		{/each}
	</div>
</nav>

<style>
	nav {
		display: flex;
		flex-direction: column;
		height: 100%;
		background: var(--surface);
		min-width: 0;
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 6px 6px 6px 12px;
		border-bottom: 1px solid var(--border);
	}
	h2 {
		margin: 0;
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--muted);
	}
	.list {
		overflow: auto;
		flex: 1;
		padding: 4px 0;
	}
	.conn {
		display: flex;
		align-items: center;
		height: 30px;
		padding-right: 4px;
	}
	.conn:hover {
		background: var(--hover);
	}
	.name {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: 1;
		min-width: 0;
		height: 100%;
		border: 0;
		background: none;
		cursor: pointer;
		padding: 0 4px 0 8px;
		text-align: left;
		font-weight: 500;
	}
	.twisty {
		width: 10px;
		color: var(--muted);
		font-size: 11px;
	}
	.text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	/* Hidden until hover or focus, but still reachable by keyboard and screen readers. */
	.actions {
		display: flex;
		flex: none;
		opacity: 0;
	}
	.actions .btn {
		width: 24px;
		height: 24px;
		font-size: 12px;
	}
	.conn:hover .actions,
	.conn:focus-within .actions {
		opacity: 1;
	}
	.confirm {
		margin: 4px 8px;
		padding: 8px;
		border: 1px solid var(--danger);
		border-radius: var(--radius);
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.confirm span {
		display: flex;
		gap: 6px;
	}
	.empty {
		padding: 12px;
		color: var(--muted);
	}
</style>
