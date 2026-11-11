<script lang="ts">
	import { api, ApiError, type Cell, type Change, type Field, type Filter, type Layout, type Plan } from '#lib/api.ts';
	import { untrack } from 'svelte';
	import { app, type Tab } from '#lib/state.svelte.ts';
	import Confirm from './Confirm.svelte';
	import Grid from './Grid.svelte';

	let { tab }: { tab: Extract<Tab, { kind: 'browse' }> } = $props();

	const PAGE = 200;
	const ops = [
		{ op: '~', label: 'contains' },
		{ op: '=', label: '=' },
		{ op: '!=', label: '≠' },
		{ op: '<', label: '<' },
		{ op: '<=', label: '≤' },
		{ op: '>', label: '>' },
		{ op: '>=', label: '≥' },
		{ op: 'null', label: 'is null' },
		{ op: '!null', label: 'is not null' }
	];

	let layout = $state<Layout | null>(null);
	let fields = $state<Field[]>([]);
	// A plain array plus a version counter keeps big pages out of deep reactivity.
	const rows: Cell[][] = [];
	let version = $state(0);
	let loading = $state(false);
	let done = $state(false);
	let error = $state('');
	let sort = $state<{ field: string; desc: boolean } | null>(null);
	let filters = $state<Filter[]>([]);
	let draft = $state<{ field: string; op: string; value: string }>({ field: '', op: '~', value: '' });
	let pending = new Map<string, Cell>();
	let pendingVersion = $state(0);
	let plan = $state<Plan | null>(null);
	let applying = $state(false);
	let applyError = $state('');
	let request = 0;
	// The server's token for the next page, for stores that page by cursor rather than by key.
	let cursor: string | undefined;

	const profile = $derived(app.profile(tab.profile));
	const production = $derived(profile?.env === 'production');
	const editableTable = $derived(!!layout && layout.key.length > 0 && !profile?.readOnly);
	const pendingCount = $derived((void pendingVersion, pending.size));
	const loaded = $derived((void version, rows.length));

	async function load(more = false) {
		if (loading && more) return;
		const id = ++request;
		loading = true;
		error = '';
		try {
			const last = rows[rows.length - 1];
			const res = await api.browse(tab.profile, {
				ref: tab.ref,
				sort: sort ? [sort] : [],
				filter: filters,
				limit: PAGE,
				after: more && !cursor && layout?.key.length ? last : undefined,
				offset: more && !cursor && !layout?.key.length ? rows.length : 0,
				cursor: more ? cursor : undefined
			});
			if (id !== request) return;
			layout = res.layout;
			if (!more) {
				fields = res.fields ?? res.layout.fields;
				rows.length = 0;
			}
			for (const r of res.rows) rows.push(r);
			cursor = res.cursor;
			done = !res.more;
			version++;
		} catch (e) {
			if (id === request) error = (e as Error).message;
		} finally {
			if (id === request) loading = false;
		}
	}

	function reload() {
		pending.clear();
		pendingVersion++;
		done = false;
		load(false);
	}

	$effect(() => {
		void tab.ref;
		untrack(reload);
	});

	function guardPending() {
		return pending.size === 0 || confirm(`Discard ${pending.size} unsaved change(s)?`);
	}

	function toggleSort(field: string) {
		if (!guardPending()) return;
		if (sort?.field !== field) sort = { field, desc: false };
		else if (!sort.desc) sort = { field, desc: true };
		else sort = null;
		reload();
	}

	function addFilter(e: SubmitEvent) {
		e.preventDefault();
		if (!draft.field || !guardPending()) return;
		const needsValue = draft.op !== 'null' && draft.op !== '!null';
		filters.push({ field: draft.field, op: draft.op, value: needsValue ? draft.value : null });
		draft.value = '';
		reload();
	}

	function removeFilter(i: number) {
		if (!guardPending()) return;
		filters.splice(i, 1);
		reload();
	}

	function editable(r: number, c: number) {
		if (!editableTable) return false;
		const v = rows[r]?.[c];
		return fields[c]?.kind !== 'bytes' && (v === null || typeof v === 'string');
	}

	function onedit(r: number, c: number, v: Cell) {
		const k = `${r}:${c}`;
		if (v === rows[r][c]) pending.delete(k);
		else pending.set(k, v);
		pendingVersion++;
	}

	function changes(): Change[] {
		const byRow = new Map<number, Change>();
		const keyIdx = layout!.key.map((k) => fields.findIndex((f) => f.name === k));
		for (const [k, v] of pending) {
			const [r, c] = k.split(':').map(Number);
			let ch = byRow.get(r);
			if (!ch) {
				ch = { key: keyIdx.map((i) => rows[r][i]), old: {}, set: {} };
				byRow.set(r, ch);
			}
			ch.old[fields[c].name] = rows[r][c];
			ch.set[fields[c].name] = v;
		}
		return [...byRow.entries()].sort((a, b) => a[0] - b[0]).map(([, ch]) => ch);
	}

	async function preview() {
		applyError = '';
		try {
			plan = await api.plan(tab.profile, tab.ref, changes());
		} catch (e) {
			app.notify((e as Error).message, 'error');
		}
	}

	async function apply() {
		if (!plan) return;
		applying = true;
		applyError = '';
		try {
			// The preview dialog is the confirmation, typed on production.
			await api.apply(tab.profile, plan.id, true);
			const n = plan.steps.length;
			plan = null;
			app.notify(`Applied ${n} change${n === 1 ? '' : 's'}`);
			reload();
		} catch (e) {
			applyError =
				e instanceof ApiError && e.code === 'conflict'
					? `${e.message}\n\nReload to see the current values, then edit again.`
					: (e as Error).message;
		} finally {
			applying = false;
		}
	}

	function discard() {
		pending.clear();
		pendingVersion++;
	}
</script>

<div class="browse">
	<div class="toolbar">
		<button class="btn" onclick={() => guardPending() && reload()} title="Reload" disabled={loading}>⟳ Reload</button>
		<form class="filter" onsubmit={addFilter}>
			<select class="input" bind:value={draft.field} aria-label="Filter column">
				<option value="">Filter column…</option>
				{#each layout?.fields ?? [] as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
			</select>
			<select class="input" bind:value={draft.op} aria-label="Filter operator">
				{#each ops as o (o.op)}<option value={o.op}>{o.label}</option>{/each}
			</select>
			{#if draft.op !== 'null' && draft.op !== '!null'}
				<input class="input" bind:value={draft.value} placeholder="value" aria-label="Filter value" />
			{/if}
			<button class="btn" type="submit" disabled={!draft.field}>Add filter</button>
		</form>
		<span class="spacer"></span>
		{#if pendingCount > 0}
			<span class="badge staging">{pendingCount} pending</span>
			<button class="btn" onclick={discard}>Discard</button>
			<button class="btn primary" onclick={preview}>Preview and apply…</button>
		{/if}
	</div>

	{#if filters.length}
		<div class="chips">
			{#each filters as f, i (i)}
				<span class="chip mono">
					{f.field}
					{ops.find((o) => o.op === f.op)?.label}
					{#if f.value !== null}<q>{f.value}</q>{/if}
					<button aria-label="Remove filter" onclick={() => removeFilter(i)}>✕</button>
				</span>
			{/each}
		</div>
	{/if}

	{#if layout && !editableTable}
		<div class="note">
			{#if profile?.readOnly}Read-only connection: editing is off.{:else}Not editable: {layout.note || 'no primary key'}.{/if}
		</div>
	{/if}

	<div class="grid-wrap">
		{#if error}
			<div class="pad"><div class="error-box" role="alert">{error}</div></div>
		{:else if fields.length}
			<Grid
				{fields}
				{rows}
				{version}
				{sort}
				onsort={toggleSort}
				{editable}
				{pending}
				{pendingVersion}
				{onedit}
				onnearend={() => !done && !loading && load(true)}
				label={tab.title}
			/>
		{:else if loading}
			<div class="pad muted">Loading…</div>
		{/if}
	</div>

	<div class="status">
		<span>{loaded.toLocaleString()}{done ? '' : '+'} rows loaded</span>
		{#if layout && layout.estimate >= 0}<span class="muted">about {layout.estimate.toLocaleString()} in table</span>{/if}
		{#if loading}<span class="muted">loading…</span>{/if}
		<span class="spacer"></span>
		{#if editableTable}<span class="muted">Double-click or Enter to edit a cell</span>{/if}
	</div>
</div>

{#if plan}
	<Confirm
		title="Apply {plan.steps.length} change{plan.steps.length === 1 ? '' : 's'}"
		profile={tab.profile}
		{production}
		action={applying ? 'Applying…' : 'Apply'}
		busy={applying}
		onconfirm={apply}
		oncancel={() => (plan = null)}
	>
		<p>
			This exact SQL will run{plan.atomic ? ' in one transaction; if any row changed since you read it, nothing is applied' : ''}.
		</p>
		<pre class="preview mono">{plan.steps.map((s) => s.preview + ';').join('\n')}</pre>
		{#if applyError}<div class="error-box" role="alert">{applyError}</div>{/if}
	</Confirm>
{/if}

<style>
	.browse {
		display: flex;
		flex-direction: column;
		height: 100%;
		min-height: 0;
		background: var(--surface);
	}
	.toolbar {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 8px;
		border-bottom: 1px solid var(--border);
		flex-wrap: wrap;
	}
	.filter {
		display: flex;
		gap: 4px;
		align-items: center;
	}
	.filter .input {
		height: 28px;
		max-width: 160px;
	}
	.spacer {
		flex: 1;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		padding: 6px 8px;
		border-bottom: 1px solid var(--border);
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 2px 4px 2px 8px;
		border: 1px solid var(--border);
		border-radius: 12px;
		background: var(--surface-2);
		font-size: 12px;
	}
	.chip button {
		border: 0;
		background: none;
		cursor: pointer;
		color: var(--muted);
	}
	.note {
		padding: 6px 10px;
		color: var(--muted);
		border-bottom: 1px solid var(--border);
		background: var(--surface-2);
	}
	.grid-wrap {
		flex: 1;
		min-height: 0;
	}
	.pad {
		padding: 12px;
	}
	.status {
		display: flex;
		gap: 12px;
		padding: 4px 10px;
		border-top: 1px solid var(--border);
		font-size: 12px;
	}
	.preview {
		margin: 8px 0;
		padding: 10px;
		background: var(--surface-2);
		border-radius: var(--radius);
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 40vh;
		overflow: auto;
	}
</style>
