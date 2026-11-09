<script lang="ts">
	import { api, ApiError, type Cell, type ExecEvent, type Field, type Prompt } from '#lib/api.ts';
	import { app, type Tab } from '#lib/state.svelte.ts';
	import Confirm from './Confirm.svelte';
	import Editor from './Editor.svelte';
	import Grid from './Grid.svelte';

	let { tab }: { tab: Extract<Tab, { kind: 'sql' }> } = $props();

	type Result = {
		stmt: number;
		fields: Field[] | null;
		version: number;
		affected: number | null;
		tag: string;
		truncated: boolean;
		error: string;
		done: boolean;
	};

	let results = $state<Result[]>([]);
	let shown = $state(0);
	let running = $state(false);
	let inTx = $state(false);
	let error = $state('');
	let elapsed = $state(0);
	let prompts = $state<{ text: string; list: Prompt[] } | null>(null);
	let controller: AbortController | null = null;
	let split = $state(45);

	const profile = $derived(app.profile(tab.profile));
	const dialect = $derived(profile?.tile ?? 'postgres');
	const current = $derived(results[shown]);

	// Rows go into plain arrays, and a version counter tells the grid to redraw, so a large
	// result does not become deeply reactive.
	const rowStore = new Map<number, Cell[][]>();

	function result(stmt: number): Result {
		const i = results.findIndex((x) => x.stmt === stmt);
		if (i >= 0) return results[i];
		results.push({ stmt, fields: null, version: 0, affected: null, tag: '', truncated: false, error: '', done: false });
		return results[results.length - 1];
	}

	function onEvent(e: ExecEvent) {
		switch (e.t) {
			case 'schema': {
				const r = result(e.stmt);
				const rows: Cell[][] = [];
				rowStore.set(e.stmt, rows);
				r.fields = e.fields;
				if (results.filter((x) => x.fields).length === 1) shown = results.indexOf(r);
				break;
			}
			case 'rows': {
				const r = result(e.stmt);
				const rows = rowStore.get(e.stmt)!;
				for (const row of e.rows) rows.push(row);
				r.version++;
				break;
			}
			case 'done': {
				const r = result(e.stmt);
				r.done = true;
				if (e.hasAffected) r.affected = e.affected ?? 0;
				r.tag = e.tag ?? '';
				break;
			}
			case 'truncated':
				result(e.stmt).truncated = true;
				break;
			case 'error': {
				const r = result(e.stmt);
				r.error = e.error;
				shown = results.indexOf(r);
				break;
			}
			case 'end':
				inTx = !!e.inTx;
				if (e.ddl) app.refreshTree(tab.profile);
				break;
		}
	}

	async function run(text: string, confirm: string[] = []) {
		if (running || !text.trim()) return;
		running = true;
		error = '';
		results = [];
		rowStore.clear();
		shown = 0;
		const started = performance.now();
		const tick = setInterval(() => (elapsed = performance.now() - started), 100);
		controller = new AbortController();
		try {
			await api.exec(tab.profile, tab.id, text, confirm, onEvent, controller.signal);
		} catch (e) {
			if (e instanceof ApiError && e.code === 'confirm' && e.prompts.length) {
				prompts = { text, list: e.prompts };
			} else if ((e as Error).name === 'AbortError') {
				const r = results[results.length - 1];
				if (r) r.error ||= 'Cancelled.';
				else error = 'Cancelled.';
			} else {
				error = (e as Error).message;
			}
		} finally {
			clearInterval(tick);
			elapsed = performance.now() - started;
			running = false;
			controller = null;
		}
	}

	function cancel() {
		controller?.abort();
	}

	function confirmed() {
		const p = prompts!;
		prompts = null;
		run(
			p.text,
			p.list.map((x) => x.statement)
		);
	}

	const rowsOf = (r: Result) => (void r.version, rowStore.get(r.stmt) ?? []);

	function summary(r: Result) {
		if (r.error) return 'Error';
		if (r.fields) return `${rowsOf(r).length.toLocaleString()}${r.truncated ? '+' : ''} rows`;
		if (r.affected !== null) return `${r.affected.toLocaleString()} affected`;
		return r.done ? 'OK' : '…';
	}

	function startSplit(e: PointerEvent) {
		const target = e.currentTarget as HTMLElement;
		const box = target.parentElement!.getBoundingClientRect();
		target.setPointerCapture(e.pointerId);
		const move = (ev: PointerEvent) => (split = Math.min(85, Math.max(15, ((ev.clientY - box.top) / box.height) * 100)));
		const up = () => {
			target.removeEventListener('pointermove', move);
			target.removeEventListener('pointerup', up);
		};
		target.addEventListener('pointermove', move);
		target.addEventListener('pointerup', up);
	}
</script>

<div class="sql" style:--split="{split}%">
	<div class="toolbar">
		{#if running}
			<button class="btn danger" onclick={cancel} title="Cancel (Esc)">■ Cancel</button>
		{:else}
			<button class="btn primary" onclick={() => run(tab.sql)} title="Run (Ctrl+Enter)">▶ Run</button>
		{/if}
		<span class="muted hint">Ctrl+Enter runs the selection, or the whole script</span>
		<span class="spacer"></span>
		{#if inTx}<span class="badge staging" title="A transaction is open in this tab">transaction open</span>{/if}
		{#if running || results.length}<span class="muted">{(elapsed / 1000).toFixed(2)} s</span>{/if}
	</div>
	<div class="editor-pane">
		<Editor bind:value={tab.sql} {dialect} onrun={(t) => run(t)} oncancel={cancel} />
	</div>
	<div class="splitter" role="separator" aria-orientation="horizontal" onpointerdown={startSplit}></div>
	<div class="results">
		{#if error}
			<div class="pad"><div class="error-box" role="alert">{error}</div></div>
		{:else if results.length === 0}
			<div class="pad muted">{running ? 'Running…' : 'Results appear here.'}</div>
		{:else}
			<div class="rtabs" role="tablist">
				{#each results as r, i (r.stmt)}
					<button role="tab" class="rtab" class:on={i === shown} class:err={!!r.error} aria-selected={i === shown} onclick={() => (shown = i)}>
						{i + 1}. {summary(r)}
					</button>
				{/each}
			</div>
			{#if current}
				{#if current.error}
					<div class="pad"><div class="error-box" role="alert">{current.error}</div></div>
				{/if}
				{#if current.fields}
					{#if current.truncated}
						<div class="note">
							Stopped at {rowsOf(current).length.toLocaleString()} rows and cancelled the query. Use <code>tess query</code> to export everything.
						</div>
					{/if}
					<div class="grid-wrap">
						<Grid fields={current.fields} rows={rowsOf(current)} version={current.version} label="Query results" />
					</div>
				{:else if !current.error}
					<div class="pad">
						{current.tag || 'Statement finished.'}
						{#if current.affected !== null}{current.affected.toLocaleString()} rows affected.{/if}
					</div>
				{/if}
			{/if}
		{/if}
	</div>
</div>

{#if prompts}
	<Confirm
		title="Confirm before running"
		profile={tab.profile}
		production={prompts.list.some((p) => p.env === 'production')}
		onconfirm={confirmed}
		oncancel={() => (prompts = null)}
	>
		<p>Nothing has run yet. These statements need your confirmation:</p>
		<ul class="prompts">
			{#each prompts.list as p (p.statement)}
				<li>
					<span class="badge {p.risk === 'destructive' ? 'production' : 'staging'}">{p.verb || p.risk}</span>
					<span class="reason">{p.reason}</span>
					<pre class="mono">{p.statement}</pre>
				</li>
			{/each}
		</ul>
	</Confirm>
{/if}

<style>
	.sql {
		display: grid;
		grid-template-rows: auto var(--split) 5px 1fr;
		height: 100%;
		min-height: 0;
	}
	.toolbar {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 8px;
		border-bottom: 1px solid var(--border);
		background: var(--surface);
	}
	.spacer {
		flex: 1;
	}
	.editor-pane {
		min-height: 0;
		overflow: hidden;
	}
	.splitter {
		background: var(--plaster);
		cursor: row-resize;
		border-top: 1px solid var(--border);
		border-bottom: 1px solid var(--border);
	}
	.results {
		display: flex;
		flex-direction: column;
		min-height: 0;
		background: var(--surface);
	}
	.rtabs {
		display: flex;
		gap: 2px;
		padding: 4px 6px 0;
		border-bottom: 1px solid var(--border);
		overflow-x: auto;
		flex: none;
	}
	.rtab {
		border: 1px solid transparent;
		border-bottom: 0;
		background: none;
		padding: 4px 10px;
		border-radius: var(--radius) var(--radius) 0 0;
		cursor: pointer;
		color: var(--muted);
		white-space: nowrap;
	}
	.rtab.on {
		background: var(--surface-2);
		border-color: var(--border);
		color: var(--text);
	}
	.rtab.err {
		color: var(--danger);
	}
	.grid-wrap {
		flex: 1;
		min-height: 0;
	}
	.pad {
		padding: 12px;
	}
	.note {
		padding: 6px 12px;
		background: var(--pending);
		border-bottom: 1px solid var(--border);
	}
	.prompts {
		list-style: none;
		padding: 0;
		margin: 8px 0 0;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.prompts pre {
		margin: 6px 0 0;
		padding: 8px;
		background: var(--surface-2);
		border-radius: var(--radius);
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 160px;
		overflow: auto;
	}
	.reason {
		margin-left: 6px;
		color: var(--muted);
	}
	@media (max-width: 700px) {
		.hint {
			display: none;
		}
	}
</style>
