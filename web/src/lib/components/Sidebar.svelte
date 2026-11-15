<script lang="ts">
	import { api, type Profile } from '#lib/api.ts';
	import { envOf, filterProfiles, summary } from '#lib/connections.ts';
	import { app } from '#lib/state.svelte.ts';
	import Icon from './Icon.svelte';
	import TileMark from './TileMark.svelte';
	import TreeLevel from './TreeLevel.svelte';

	let { onedit }: { onedit: (p?: Profile) => void } = $props();

	let deleting = $state<string | null>(null);
	let query = $state('');
	const shown = $derived(filterProfiles(app.profiles, query));

	async function remove(name: string) {
		try {
			app.closeProfileTabs(name);
			await api.deleteProfile(name);
			app.forget(name);
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
		<button class="btn primary new" onclick={() => onedit()}><Icon name="plus" /> New connection</button>
		<label class="filter">
			<Icon name="search" size={14} />
			<input class="input" type="search" placeholder="Filter connections" aria-label="Filter connections" bind:value={query} />
		</label>
	</div>
	<h2>Saved <span class="count">{app.profiles.length}</span></h2>
	<div class="list">
		{#each shown as p (p.name)}
			{@const env = envOf(p.env)}
			<section class="env-{env.color}">
				<div class="conn">
					<button class="name" aria-expanded={!!app.expanded[p.name]} title={summary(p) || p.name}
						onclick={() => (app.expanded[p.name] = !app.expanded[p.name])}>
						<span class="twisty"><Icon name={app.expanded[p.name] ? 'chevronDown' : 'chevronRight'} size={14} /></span>
						<TileMark tile={p.tile} size={16} />
						<span class="text">{p.name}</span>
						{#if env.value}<span class="badge env" title={env.label}>{env.short}</span>{/if}
						{#if p.readOnly}<span class="badge ro" title="Read-only">ro</span>{/if}
					</button>
					<span class="actions">
						<button class="btn ghost icon" title="New query tab" aria-label="New query tab for {p.name}" onclick={() => app.openSQL(p.name)}><Icon name="query" size={15} /></button>
						<button class="btn ghost icon" title="Edit" aria-label="Edit {p.name}" onclick={() => onedit(p)}><Icon name="edit" size={15} /></button>
						<button class="btn ghost icon" title="Delete" aria-label="Delete {p.name}" onclick={() => (deleting = p.name)}><Icon name="trash" size={15} /></button>
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
				{#if app.expanded[p.name]}<TreeLevel profile={p.name} parent={[]} depth={1} />{/if}
			</section>
		{:else}
			<p class="empty">{app.profiles.length ? 'No connection matches.' : 'No connections yet.'}</p>
		{/each}
	</div>
	<footer class="muted" title="tess version">Tessera {app.version}</footer>
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
		flex-direction: column;
		gap: 8px;
		padding: 10px;
	}
	.new {
		justify-content: center;
		height: 32px;
	}
	.filter {
		position: relative;
		display: flex;
		align-items: center;
		color: var(--muted);
	}
	.filter :global(.icon) {
		position: absolute;
		left: 8px;
		pointer-events: none;
	}
	.filter .input {
		width: 100%;
		padding-left: 28px;
	}
	h2 {
		display: flex;
		align-items: center;
		gap: 6px;
		margin: 4px 12px 4px;
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--muted);
	}
	.count {
		padding: 0 6px;
		border-radius: 8px;
		background: var(--surface-2);
		letter-spacing: 0;
	}
	.list {
		overflow: auto;
		flex: 1;
		padding: 0 0 4px;
	}
	/* The environment colour runs down the left edge of the connection and its tree. */
	section {
		border-left: 3px solid var(--env);
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
		padding: 0 4px 0 5px;
		text-align: left;
		font-weight: 500;
	}
	.twisty {
		color: var(--muted);
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
		padding: 0 12px;
		color: var(--muted);
	}
	footer {
		padding: 6px 12px;
		border-top: 1px solid var(--border);
		font-size: 11px;
	}
</style>
