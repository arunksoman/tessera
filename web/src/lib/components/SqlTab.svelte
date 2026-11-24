<script lang="ts">
	import { api, ApiError, type Cell, type ExecEvent, type Field, type Prompt } from '#lib/api.ts';
	import type { MenuItem } from '#lib/menu.ts';
	import { beginStatement, resultCSV, resultJSON, resultJSONL } from '#lib/results.ts';
	import { app, type Tab } from '#lib/state.svelte.ts';
	import Confirm from './Confirm.svelte';
	import ContextMenu from './ContextMenu.svelte';
	import Icon from './Icon.svelte';
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
	let editor: Editor;
	let exportMenu = $state<{ x: number; y: number; items: MenuItem[] } | null>(null);
	// Statements the server numbers before the user's own, such as the BEGIN of manual commit.
	let shift = 0;

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
		if (e.t !== 'end') {
			e = { ...e, stmt: e.stmt - shift };
			if (e.stmt < 0) {
				if (e.t === 'error') error = e.error;
				return;
			}
		}
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
		// In manual commit, open a transaction first. Same line, so error line numbers still match.
		shift = tab.manual && !inTx ? 1 : 0;
		const sent = shift ? `${beginStatement(dialect)}; ${text}` : text;
		running = true;
		error = '';
		results = [];
		rowStore.clear();
		shown = 0;
		const started = performance.now();
		const tick = setInterval(() => (elapsed = performance.now() - started), 100);
		controller = new AbortController();
		try {
			await api.exec(tab.profile, tab.id, sent, confirm, onEvent, controller.signal);
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

	/** Runs COMMIT or ROLLBACK, keeping the results on screen. */
	async function finish(stmt: 'COMMIT' | 'ROLLBACK') {
		if (running) return;
		running = true;
		controller = new AbortController();
		let failed = false;
		try {
			await api.exec(
				tab.profile,
				tab.id,
				stmt,
				[],
				(e) => {
					if (e.t === 'end') inTx = !!e.inTx;
					if (e.t === 'error') {
						failed = true;
						app.notify(e.error, 'error');
					}
				},
				controller.signal
			);
			if (!failed) app.notify(stmt === 'COMMIT' ? 'Committed.' : 'Rolled back.');
		} catch (e) {
			app.notify((e as Error).message, 'error');
		} finally {
			running = false;
			controller = null;
		}
	}

	function download(r: Result, ext: string, type: string, body: string) {
		const name = `${tab.title}-${results.indexOf(r) + 1}`.replace(/[^\w.-]+/g, '_');
		const url = URL.createObjectURL(new Blob([body], { type }));
		const a = document.createElement('a');
		a.href = url;
		a.download = `${name}.${ext}`;
		document.body.append(a);
		a.click();
		a.remove();
		setTimeout(() => URL.revokeObjectURL(url), 1000);
	}

	/** Opens the export menu for one result, under its button or at the pointer. */
	function openExport(r: Result | undefined, e: MouseEvent) {
		if (!r?.fields || running) return;
		e.preventDefault();
		const f = r.fields;
		const box = (e.currentTarget as HTMLElement).getBoundingClientRect();
		// A context-menu key press has no pointer position, so it opens under the tab.
		const atPointer = e.type === 'contextmenu' && (e.clientX || e.clientY);
		exportMenu = {
			x: atPointer ? e.clientX : box.left,
			y: atPointer ? e.clientY : box.bottom + 2,
			items: [
				{ label: 'CSV', icon: 'download', hint: 'formulas neutralized', action: () => download(r, 'csv', 'text/csv', resultCSV(f, rowsOf(r), true)) },
				{ label: 'JSON', icon: 'download', action: () => download(r, 'json', 'application/json', resultJSON(f, rowsOf(r))) },
				{ label: 'JSON Lines', icon: 'download', action: () => download(r, 'jsonl', 'application/x-ndjson', resultJSONL(f, rowsOf(r))) }
			]
		};
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

	const shortcuts: string[][] = [
		['Run all, or the selection', 'Ctrl+Enter', 'F5'],
		['Run current statement', 'Ctrl+Shift+Enter', 'Shift+F5'],
		['Cancel', 'Esc'],
		['Autocomplete', 'Ctrl+Space'],
		['Format', 'Ctrl+Shift+F'],
		['Find', 'Ctrl+F'],
		['Undo', 'Ctrl+Z']
	];

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
		<div class="seg" role="radiogroup" aria-label="Commit mode">
			<button
				role="radio"
				aria-checked={!tab.manual}
				class:on={!tab.manual}
				disabled={inTx}
				title={inTx ? 'Commit or roll back first' : 'Every statement commits by itself'}
				onclick={() => (tab.manual = false)}>Auto commit</button
			>
			<button
				role="radio"
				aria-checked={!!tab.manual}
				class:on={tab.manual}
				title="A run opens a transaction; nothing is saved until you commit"
				onclick={() => (tab.manual = true)}>Manual commit</button
			>
		</div>
		{#if tab.manual || inTx}
			<button class="btn" disabled={!inTx || running} onclick={() => finish('COMMIT')}>Commit</button>
			<button class="btn" disabled={!inTx || running} onclick={() => finish('ROLLBACK')}><Icon name="undo" size={14} /> Rollback</button>
		{/if}
		<button class="btn ghost" onclick={() => editor.format()} title="Format the selection, or the whole script (Ctrl+Shift+F)">Format</button>
		<span class="spacer"></span>
		{#if inTx}<span class="badge staging" title="A transaction is open in this tab">transaction open</span>{/if}
		{#if running || results.length}<span class="muted">{(elapsed / 1000).toFixed(2)} s</span>{/if}
	</div>
	<div class="editor-pane">
		<Editor bind:this={editor} bind:value={tab.sql} {dialect} onrun={(t) => run(t)} oncancel={cancel} />
	</div>
	<div class="splitter" role="separator" aria-orientation="horizontal" onpointerdown={startSplit}></div>
	<div class="results">
		{#if error}
			<div class="pad"><div class="error-box" role="alert">{error}</div></div>
		{:else if results.length === 0}
			{#if running}
				<div class="pad muted">Running…</div>
			{:else}
				<dl class="keys" aria-label="Keyboard shortcuts">
					{#each shortcuts as [label, ...combos] (label)}
						<dt>{label}</dt>
						<dd>
							{#each combos as combo, i (combo)}
								{#if i > 0}<span class="muted">or</span>{/if}
								<span class="combo">{#each combo.split('+') as k, j (j)}<kbd>{k}</kbd>{/each}</span>
							{/each}
						</dd>
					{/each}
				</dl>
			{/if}
		{:else}
			<div class="rbar">
				<div class="rtabs" role="tablist">
					{#each results as r, i (r.stmt)}
						<button
							role="tab"
							class="rtab"
							class:on={i === shown}
							class:err={!!r.error}
							aria-selected={i === shown}
							title={r.fields ? 'Right-click to export' : undefined}
							onclick={() => (shown = i)}
							oncontextmenu={(e) => openExport(r, e)}
						>
							{i + 1}. {summary(r)}
						</button>
					{/each}
				</div>
				{#if current?.fields}
					<button
						class="btn ghost export"
						disabled={running}
						onclick={(e) => openExport(current, e)}
						aria-label="Export result {shown + 1}"
						title="Download result {shown + 1} as CSV, JSON, or JSON Lines"
					>
						<Icon name="download" size={14} /> Export{#if results.length > 1}&nbsp;{shown + 1}{/if}
					</button>
				{/if}
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

{#if exportMenu}
	<ContextMenu x={exportMenu.x} y={exportMenu.y} items={exportMenu.items} label="Export result" onclose={() => (exportMenu = null)} />
{/if}

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
	.seg {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		overflow: hidden;
	}
	.seg button {
		height: 26px;
		padding: 0 10px;
		border: 0;
		background: var(--surface);
		color: var(--muted);
		cursor: pointer;
	}
	.seg button.on {
		background: var(--selected);
		color: var(--text);
		font-weight: 600;
	}
	.seg button:disabled {
		cursor: default;
	}
	.keys {
		display: grid;
		grid-template-columns: auto auto;
		gap: 10px 16px;
		align-items: center;
		margin: 32px auto;
		width: max-content;
		max-width: 100%;
		color: var(--muted);
	}
	.keys dd {
		margin: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
	}
	.combo {
		display: inline-flex;
		gap: 3px;
	}
	kbd {
		font-family: var(--font-mono);
		font-size: 11px;
		padding: 1px 6px;
		border: 1px solid var(--border);
		border-bottom-width: 2px;
		border-radius: 4px;
		background: var(--surface-2);
		color: var(--text);
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
	.rbar {
		display: flex;
		align-items: flex-end;
		gap: 6px;
		border-bottom: 1px solid var(--border);
		flex: none;
	}
	.rtabs {
		display: flex;
		gap: 2px;
		padding: 4px 6px 0;
		overflow-x: auto;
		flex: 1;
		min-width: 0;
	}
	/* An accent tint, quieter than Run's solid fill; text stays at AA contrast in both themes. */
	.export {
		height: 24px;
		margin: 0 6px 3px 0;
		flex: none;
		color: var(--lapis);
		background: color-mix(in srgb, var(--lapis) 12%, transparent);
		border-color: color-mix(in srgb, var(--lapis) 35%, transparent);
		font-weight: 600;
	}
	.export:hover:not(:disabled) {
		background: color-mix(in srgb, var(--lapis) 20%, transparent);
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
		.toolbar {
			flex-wrap: wrap;
		}
	}
</style>
