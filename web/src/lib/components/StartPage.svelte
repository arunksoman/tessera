<script lang="ts">
	import type { Profile } from '#lib/api.ts';
	import { colorOf, relativeTime, summary } from '#lib/connections.ts';
	import { app } from '#lib/state.svelte.ts';
	import Icon from './Icon.svelte';
	import TileMark from './TileMark.svelte';

	let { loaded, onnew, onedit }: { loaded: boolean; onnew: () => void; onedit: (p: Profile) => void } = $props();

	// Recent entries for connections that still exist, newest first.
	const recent = $derived(
		app.recent.flatMap((r) => {
			const p = app.profile(r.name);
			return p ? [{ p, at: r.at }] : [];
		})
	);
	// Re-render relative times once a minute.
	let now = $state(Date.now());
	$effect(() => {
		const t = setInterval(() => (now = Date.now()), 60_000);
		return () => clearInterval(t);
	});
</script>

{#snippet item(p: Profile, detail: string)}
	<li class="conn-{colorOf(p.color)}">
		<button class="open" onclick={() => app.connect(p.name)} title="Open a SQL tab on {p.name}">
			<TileMark tile={p.tile} size={22} />
			<span class="what">
				<span class="name">{p.name}</span>
				<span class="detail muted">{detail}</span>
			</span>
			{#if p.readOnly}<span class="badge ro">ro</span>{/if}
		</button>
		<button class="btn ghost icon edit" aria-label="Edit {p.name}" title="Edit" onclick={() => onedit(p)}><Icon name="edit" size={15} /></button>
	</li>
{/snippet}

<div class="start">
	<header>
		<h1>Welcome to Tessera</h1>
		{#if loaded && app.profiles.length === 0}
			<p class="muted">Add a PostgreSQL, MySQL, MariaDB, or SQLite connection to start browsing tables and running SQL.</p>
		{:else}
			<p class="muted">Open a saved connection, or add a new one.</p>
		{/if}
		<button class="btn primary big" onclick={onnew}><Icon name="plus" /> New connection</button>
	</header>

	{#if !loaded}
		<p class="muted">Loading…</p>
	{:else if app.profiles.length}
		<div class="lists">
			<section aria-labelledby="saved-h">
				<h2 id="saved-h">Saved <span class="count">{app.profiles.length}</span></h2>
				<ul>
					{#each app.profiles as p (p.name)}
						{@render item(p, summary(p) || (app.tile(p.tile)?.name ?? p.tile))}
					{/each}
				</ul>
			</section>
			<section aria-labelledby="recent-h">
				<h2 id="recent-h"><Icon name="clock" size={13} /> Recent <span class="count">{recent.length}</span></h2>
				{#if recent.length}
					<ul>
						{#each recent as r (r.p.name)}
							{@render item(r.p, relativeTime(r.at, now))}
						{/each}
					</ul>
				{:else}
					<p class="muted none">Connections you open appear here.</p>
				{/if}
			</section>
		</div>
		<p class="muted keys">Alt+T new SQL tab · Alt+W close tab · Alt+[ and Alt+] switch tabs · Ctrl+Enter run · Esc cancel</p>
	{/if}
</div>

<style>
	.start {
		max-width: 920px;
		margin: 0 auto;
		padding: 40px 24px 24px;
		height: 100%;
		overflow: auto;
	}
	header {
		text-align: center;
		margin-bottom: 28px;
	}
	h1 {
		margin: 0 0 6px;
		font-size: 22px;
	}
	header p {
		margin: 0 0 16px;
	}
	.big {
		height: 34px;
		padding: 0 16px;
		font-weight: 600;
	}
	.lists {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
		gap: 20px;
	}
	h2 {
		display: flex;
		align-items: center;
		gap: 6px;
		margin: 0 0 8px;
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
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	/* The connection's colour is the card's left edge. */
	li {
		display: flex;
		align-items: center;
		border: 1px solid var(--border);
		border-left: 4px solid var(--conn);
		border-radius: 6px;
		background: var(--surface);
	}
	li.conn-none {
		border-left-color: var(--border);
	}
	li:hover {
		background: var(--hover);
	}
	.open {
		display: flex;
		align-items: center;
		gap: 10px;
		flex: 1;
		min-width: 0;
		padding: 8px 6px 8px 10px;
		border: 0;
		background: none;
		cursor: pointer;
		text-align: left;
	}
	.what {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
	}
	.name {
		font-weight: 600;
	}
	.name,
	.detail {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.detail {
		font-size: 12px;
	}
	.edit {
		margin-right: 6px;
		color: var(--muted);
	}
	.none {
		margin: 0;
		padding: 10px 0;
	}
	.keys {
		margin-top: 28px;
		text-align: center;
		font-size: 12px;
	}
</style>
