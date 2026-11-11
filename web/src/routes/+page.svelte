<script lang="ts">
	import { setSignedOutHandler, type Profile } from '#lib/api.ts';
	import BrowseTab from '#lib/components/BrowseTab.svelte';
	import ConnectionForm from '#lib/components/ConnectionForm.svelte';
	import Sidebar from '#lib/components/Sidebar.svelte';
	import SqlTab from '#lib/components/SqlTab.svelte';
	import TileMark from '#lib/components/TileMark.svelte';
	import { app, type Theme } from '#lib/state.svelte.ts';

	let form = $state<{ editing?: Profile } | null>(null);
	let sidebar = $state(280);
	let loaded = $state(false);

	setSignedOutHandler(() => (app.signedOut = true));
	$effect(() => {
		app.load().then(() => (loaded = true));
	});

	const tab = $derived(app.activeTab);
	const tabProfile = $derived(tab ? app.profile(tab.profile) : undefined);

	// Keys from design.md section 12. Alt+T and Alt+W, because browsers keep Ctrl+T and Ctrl+W.
	function keydown(e: KeyboardEvent) {
		if (form) return;
		if (e.altKey && !e.ctrlKey && !e.metaKey) {
			const k = e.key.toLowerCase();
			if (k === 't') {
				const p = tab?.profile ?? app.profiles[0]?.name;
				if (p) app.openSQL(p);
			} else if (k === 'w' && app.active) app.closeTab(app.active);
			else if (e.key === ']') app.cycleTab(1);
			else if (e.key === '[') app.cycleTab(-1);
			else return;
			e.preventDefault();
		}
	}

	function startResize(e: PointerEvent) {
		const target = e.currentTarget as HTMLElement;
		target.setPointerCapture(e.pointerId);
		const move = (ev: PointerEvent) => (sidebar = Math.min(600, Math.max(180, ev.clientX)));
		const up = () => {
			target.removeEventListener('pointermove', move);
			target.removeEventListener('pointerup', up);
		};
		target.addEventListener('pointermove', move);
		target.addEventListener('pointerup', up);
	}
</script>

<svelte:window onkeydown={keydown} />

{#if app.signedOut}
	<main class="center">
		<div class="card">
			<h1>Signed out</h1>
			<p>This browser's session ended, usually because tess serve restarted.</p>
			<p>Open the new link that <code>tess serve</code> printed in its terminal.</p>
		</div>
	</main>
{:else}
	<div class="shell" style:--sidebar="{sidebar}px">
		<header class="top">
			<span class="logo" aria-hidden="true">
				<i></i><i></i><i></i><i class="n"></i><i></i><i class="n"></i><i class="n"></i><i></i><i class="n"></i>
			</span>
			<strong>Tessera</strong>
			{#if app.version}<span class="muted version" title="tess version">{app.version}</span>{/if}
			<span class="spacer"></span>
			<label class="theme">
				<span class="muted">Theme</span>
				<select class="input" value={app.theme} onchange={(e) => app.setTheme(e.currentTarget.value as Theme)}>
					<option value="system">System</option>
					<option value="light">Light</option>
					<option value="dark">Dark</option>
				</select>
			</label>
		</header>

		<aside class="side">
			<Sidebar onedit={(p) => (form = { editing: p })} />
		</aside>
		<div class="gutter" role="separator" aria-orientation="vertical" aria-label="Resize sidebar" onpointerdown={startResize}></div>

		<main class="main">
			{#if app.loadError}
				<div class="pad"><div class="error-box" role="alert">Could not reach tess serve: {app.loadError}</div></div>
			{:else if app.tabs.length === 0}
				<div class="center">
					<div class="card">
						{#if loaded && app.profiles.length === 0}
							<h1>Welcome to Tessera</h1>
							<p>Add a PostgreSQL, MySQL, or MariaDB connection to start browsing tables and running SQL.</p>
							<button class="btn primary" onclick={() => (form = {})}>＋ Add a connection</button>
						{:else if loaded}
							<h1>Pick something to open</h1>
							<p>Expand a connection on the left and click a table to browse it, or open a SQL tab.</p>
							<div class="quick">
								{#each app.profiles as p (p.name)}
									<button class="btn" onclick={() => app.openSQL(p.name)}><TileMark tile={p.tile} size={16} /> SQL on {p.name}</button>
								{/each}
							</div>
							<p class="muted keys">Alt+T new SQL tab · Alt+W close tab · Alt+[ and Alt+] switch tabs · Ctrl+Enter run · Esc cancel</p>
						{:else}
							<p class="muted">Loading…</p>
						{/if}
					</div>
				</div>
			{:else}
				<div class="tabs" role="tablist">
					{#each app.tabs as t (t.id)}
						<div class="tab" class:on={t.id === app.active}>
							<button role="tab" aria-selected={t.id === app.active} class="tab-label" onclick={() => (app.active = t.id)} title={t.title}>
								<TileMark tile={app.profile(t.profile)?.tile ?? ''} size={14} />
								<span class="kind" aria-hidden="true">{t.kind === 'sql' ? '⌨' : '▦'}</span>
								<span class="text">{t.kind === 'browse' ? `${t.profile} · ${t.title}` : t.title}</span>
							</button>
							<button class="close" aria-label="Close {t.title}" onclick={() => app.closeTab(t.id)}>✕</button>
						</div>
					{/each}
				</div>
				{#if tabProfile?.env === 'production'}
					<div class="env production" role="status">PRODUCTION · {tabProfile.name}: writes ask you to type the connection name</div>
				{:else if tabProfile?.env === 'staging'}
					<div class="env staging" role="status">STAGING · {tabProfile.name}</div>
				{/if}
				<div class="panes">
					{#each app.tabs as t (t.id)}
						<!-- Tabs stay mounted, so editors and results survive switching. -->
						<div class="pane" class:on={t.id === app.active} role="tabpanel">
							{#if t.kind === 'sql'}<SqlTab tab={t} />{:else}<BrowseTab tab={t} />{/if}
						</div>
					{/each}
				</div>
			{/if}
		</main>
	</div>
{/if}

{#if form}
	<ConnectionForm editing={form.editing} onclose={() => (form = null)} />
{/if}

{#if app.toast}
	<div class="toast {app.toast.kind}" role="status">{app.toast.text}</div>
{/if}

<style>
	.shell {
		display: grid;
		grid-template-columns: var(--sidebar) 4px 1fr;
		grid-template-rows: 40px 1fr;
		height: 100%;
	}
	.top {
		grid-column: 1 / -1;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 0 12px;
		background: var(--surface);
		border-bottom: 1px solid var(--border);
	}
	.logo {
		display: grid;
		grid-template-columns: repeat(3, 6px);
		gap: 1.5px;
	}
	.logo i {
		width: 6px;
		height: 6px;
		border-radius: 1px;
		background: var(--lapis);
	}
	.logo i.n {
		background: var(--border);
	}
	.spacer {
		flex: 1;
	}
	.theme {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.theme .input {
		height: 28px;
	}
	.side {
		min-height: 0;
		border-right: 1px solid var(--border);
	}
	.gutter {
		cursor: col-resize;
		background: var(--plaster);
	}
	.main {
		display: flex;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
	}
	.tabs {
		display: flex;
		gap: 2px;
		padding: 4px 6px 0;
		background: var(--plaster);
		border-bottom: 1px solid var(--border);
		overflow-x: auto;
		flex: none;
	}
	.tab {
		display: flex;
		align-items: center;
		max-width: 260px;
		border: 1px solid transparent;
		border-bottom: 0;
		border-radius: var(--radius) var(--radius) 0 0;
	}
	.tab.on {
		background: var(--surface);
		border-color: var(--border);
	}
	.tab-label {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		border: 0;
		background: none;
		padding: 6px 4px 6px 10px;
		cursor: pointer;
		color: var(--muted);
	}
	.tab.on .tab-label {
		color: var(--text);
	}
	.kind {
		font-size: 11px;
	}
	.text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.close {
		border: 0;
		background: none;
		cursor: pointer;
		color: var(--muted);
		padding: 0 8px;
		font-size: 11px;
	}
	.close:hover {
		color: var(--text);
	}
	.env {
		padding: 4px 12px;
		font-weight: 700;
		font-size: 12px;
		letter-spacing: 0.04em;
		flex: none;
	}
	.env.production {
		background: #b3261e;
		color: #fff;
	}
	.env.staging {
		background: var(--warning);
		color: var(--on-accent);
	}
	.panes {
		flex: 1;
		min-height: 0;
		position: relative;
	}
	.pane {
		position: absolute;
		inset: 0;
		display: none;
	}
	.pane.on {
		display: block;
	}
	.center {
		display: flex;
		align-items: center;
		justify-content: center;
		height: 100%;
		padding: 16px;
	}
	.card {
		max-width: 520px;
		padding: 24px;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 6px;
	}
	.card h1 {
		margin: 0 0 8px;
		font-size: 18px;
	}
	.quick {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		margin: 12px 0;
	}
	.keys {
		font-size: 12px;
	}
	.pad {
		padding: 16px;
	}
	.toast {
		position: fixed;
		right: 16px;
		bottom: 16px;
		padding: 8px 14px;
		border-radius: var(--radius);
		background: var(--text);
		color: var(--surface);
		max-width: 420px;
		z-index: 10;
	}
	.toast.error {
		background: var(--danger);
		color: #fff;
	}
	@media (max-width: 720px) {
		.shell {
			grid-template-columns: 1fr;
			grid-template-rows: 40px 40% 0 1fr;
		}
		.side {
			border-right: 0;
			border-bottom: 1px solid var(--border);
		}
		.gutter {
			display: none;
		}
	}
</style>
