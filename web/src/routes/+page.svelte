<script lang="ts">
	import { setSignedOutHandler, type Profile } from '#lib/api.ts';
	import BrowseTab from '#lib/components/BrowseTab.svelte';
	import ConnectionForm from '#lib/components/ConnectionForm.svelte';
	import Sidebar from '#lib/components/Sidebar.svelte';
	import Icon from '#lib/components/Icon.svelte';
	import SqlTab from '#lib/components/SqlTab.svelte';
	import StartPage from '#lib/components/StartPage.svelte';
	import TileMark from '#lib/components/TileMark.svelte';
	import { colorOf, summary } from '#lib/connections.ts';
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
	const tabColor = $derived(colorOf(tabProfile?.color));

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
				<StartPage {loaded} onnew={() => (form = {})} onedit={(p) => (form = { editing: p })} />
			{:else}
				<div class="tabs" role="tablist">
					{#each app.tabs as t (t.id)}
						<div class="tab conn-{colorOf(app.profile(t.profile)?.color)}" class:on={t.id === app.active}>
							<button role="tab" aria-selected={t.id === app.active} class="tab-label" onclick={() => (app.active = t.id)} title={t.title}>
								<TileMark tile={app.profile(t.profile)?.tile ?? ''} size={14} />
								<span class="kind"><Icon name={t.kind === 'sql' ? 'query' : 'table'} size={13} /></span>
								<span class="text">{t.kind === 'browse' ? `${t.profile} · ${t.title}` : t.title}</span>
							</button>
							<button class="close" aria-label="Close {t.title}" onclick={() => app.closeTab(t.id)}><Icon name="close" size={13} /></button>
						</div>
					{/each}
				</div>
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

		<!-- One footer row under both columns, so the two halves always line up. -->
		<footer class="side-foot muted" title="tess version">Tessera {app.version}</footer>
		<footer class="main-foot conn-{tabColor}">
			{#if tabProfile}
				<TileMark tile={tabProfile.tile} size={13} />
				<strong>{tabProfile.name}</strong>
				<span class="muted">{summary(tabProfile)}</span>
				{#if tabProfile.readOnly}<span class="badge ro">read-only</span>{/if}
			{/if}
		</footer>
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
		grid-template-rows: 40px 1fr var(--footer);
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
		grid-row: 2;
		min-height: 0;
		border-right: 1px solid var(--border);
	}
	.gutter {
		grid-row: 2 / 4;
		cursor: col-resize;
		background: var(--plaster);
	}
	.main {
		grid-row: 2;
		grid-column: 3;
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
	/* Tabs carry their connection's colour along the top edge. */
	.tab:not(.conn-none) {
		box-shadow: inset 0 3px 0 var(--conn);
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
		display: inline-flex;
		color: var(--muted);
	}
	.text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.close {
		display: inline-flex;
		align-items: center;
		align-self: stretch;
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
	footer {
		grid-row: 3;
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		padding: 0 12px;
		border-top: 1px solid var(--border);
		background: var(--surface);
		font-size: 11px;
		white-space: nowrap;
		overflow: hidden;
	}
	.side-foot {
		grid-column: 1;
		border-right: 1px solid var(--border);
	}
	/* The active connection's colour fills the footer, softened so text stays readable. */
	.main-foot {
		grid-column: 3;
		background: color-mix(in srgb, var(--conn) 30%, var(--surface));
		border-top-color: var(--conn);
		box-shadow: inset 0 1px 0 var(--conn);
	}
	.main-foot.conn-none {
		background: var(--surface);
		border-top-color: var(--border);
		box-shadow: none;
	}
	.main-foot span {
		overflow: hidden;
		text-overflow: ellipsis;
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
			grid-template-rows: 40px 40% 0 1fr var(--footer);
		}
		.main,
		.main-foot {
			grid-column: 1;
		}
		.main {
			grid-row: 4;
		}
		.main-foot {
			grid-row: 5;
		}
		.side-foot {
			display: none;
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
