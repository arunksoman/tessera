<script lang="ts">
	import { untrack } from 'svelte';
	import { api, ApiError, type Plan, type Ref, type Structure, type TreeNode } from '#lib/api.ts';
	import {
		blankField,
		blankIndex,
		blankRelation,
		blankTrigger,
		changes,
		draftOf,
		joinList,
		pendingCount,
		rowState,
		splitList,
		targetLabel,
		type Draft,
		type Row,
		type Section
	} from '#lib/design.ts';
	import { app, type Tab } from '#lib/state.svelte.ts';
	import Dialog from './Dialog.svelte';
	import Icon from './Icon.svelte';

	// A table's columns, indexes, relations, and triggers, edited in place. Edits stay in the
	// page until reviewed: the server plans the SQL, the user reads it, then it runs.
	let { tab }: { tab: Extract<Tab, { kind: 'structure' }> } = $props();

	const profile = $derived(app.profile(tab.profile));
	const production = $derived(profile?.env === 'production');
	const objectName = $derived(tab.ref[tab.ref.length - 1]?.name ?? '');
	const isView = $derived(tab.ref[tab.ref.length - 1]?.kind !== 'table');
	const readOnly = $derived(!!profile?.readOnly || isView);
	const schemaRef = $derived(tab.ref.slice(0, -1));
	const schemaName = $derived(schemaRef[schemaRef.length - 1]?.name ?? '');

	let structure = $state.raw<Structure | null>(null);
	let draft = $state<Draft | null>(null);
	let loading = $state(false);
	let error = $state('');
	let section = $state<Section>('fields');
	let filter = $state('');
	let tables = $state<TreeNode[]>([]);

	let plan = $state<Plan | null>(null);
	let planError = $state('');
	let applying = $state(false);
	let typed = $state('');
	let typedProfile = $state('');

	const pending = $derived(draft ? pendingCount(draft) : 0);

	async function load() {
		loading = true;
		error = '';
		try {
			structure = await api.structure(tab.profile, tab.ref);
			draft = draftOf(structure);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			loading = false;
		}
	}

	// Tables in the same schema, for relation targets.
	async function loadTables() {
		const out: TreeNode[] = [];
		let cursor = '';
		try {
			for (let i = 0; i < 20; i++) {
				const page = await api.tree(tab.profile, schemaRef, cursor);
				out.push(...page.nodes.filter((n) => n.browsable && n.kind === 'table'));
				if (!page.next) break;
				cursor = page.next;
			}
		} catch {
			// The target list is a convenience; existing relations still show.
		}
		tables = out;
	}

	$effect(() => {
		void tab.ref;
		untrack(() => {
			load();
			loadTables();
		});
	});

	function reload() {
		if (pending && !confirm('Discard your unapplied structure changes?')) return;
		load();
	}

	const sections: { id: Section; label: string; add: string }[] = [
		{ id: 'fields', label: 'Columns', add: 'Column' },
		{ id: 'indexes', label: 'Indexes', add: 'Index' },
		{ id: 'relations', label: 'Relations', add: 'Relation' },
		{ id: 'triggers', label: 'Triggers', add: 'Trigger' }
	];

	function add() {
		if (!draft) return;
		const blank = { fields: blankField, indexes: blankIndex, relations: blankRelation, triggers: blankTrigger }[section]();
		(draft[section] as Row<unknown>[]).push({ orig: null, cur: blank, deleted: false });
	}

	function remove<T>(list: Row<T>[], i: number) {
		const r = list[i];
		if (!r.orig) list.splice(i, 1);
		else r.deleted = !r.deleted;
	}

	function shown<T extends { name: string }>(list: Row<T>[]): { r: Row<T>; i: number }[] {
		const f = filter.trim().toLowerCase();
		return list.map((r, i) => ({ r, i })).filter(({ r }) => !f || r.cur.name.toLowerCase().includes(f) || !r.orig);
	}

	function targetOf(name: string): Ref {
		return [...schemaRef, { kind: 'table', name }];
	}

	async function review() {
		if (!draft) return;
		planError = '';
		typed = '';
		typedProfile = '';
		try {
			plan = await api.planStructure(tab.profile, tab.ref, changes(draft));
		} catch (e) {
			app.notify((e as Error).message, 'error');
		}
	}

	async function toEditor() {
		if (!draft) return;
		try {
			const p = await api.planStructure(tab.profile, tab.ref, changes(draft));
			app.openSQL(tab.profile, p.steps.map((s) => s.preview + ';').join('\n') + '\n');
		} catch (e) {
			app.notify((e as Error).message, 'error');
		}
	}

	const destructive = $derived(plan?.steps.some((s) => s.risk === 'destructive') ?? false);
	const canApply = $derived(!!plan && !applying && (!destructive || typed === objectName) && (!production || typedProfile === tab.profile));

	async function apply() {
		if (!plan) return;
		applying = true;
		planError = '';
		try {
			await api.apply(tab.profile, plan.id, true);
			const n = plan.steps.length;
			plan = null;
			app.notify(`Applied ${n} statement${n === 1 ? '' : 's'} to ${objectName}.`);
			app.refreshTree(tab.profile);
			await load();
		} catch (e) {
			planError =
				e instanceof ApiError && e.code === 'conflict'
					? 'The plan expired; review the changes again.'
					: `${(e as Error).message}${plan?.atomic ? '\nNothing was changed.' : '\nStatements before the failing one stayed applied; reload to see the table as it is now.'}`;
		} finally {
			applying = false;
		}
	}

	const timings = ['BEFORE', 'AFTER', 'INSTEAD OF'];
</script>

<div class="structure">
	<div class="tabs" role="tablist" aria-label="Structure sections">
		{#each sections as s (s.id)}
			<button role="tab" class:on={section === s.id} aria-selected={section === s.id} onclick={() => (section = s.id)}>
				{s.label}
				{#if draft}<span class="count">{draft[s.id].length}</span>{/if}
			</button>
		{/each}
	</div>

	<div class="toolbar">
		<label class="search">
			<Icon name="search" size={14} />
			<input class="input" placeholder="Filter {sections.find((s) => s.id === section)?.label.toLowerCase()}" bind:value={filter} />
		</label>
		<span class="spacer"></span>
		<button class="btn ghost icon" title="Reload" aria-label="Reload" onclick={reload} disabled={loading}><Icon name="refresh" size={14} /></button>
		{#if !readOnly}
			<button class="btn primary" onclick={add} disabled={!draft}><Icon name="plus" size={14} /> {sections.find((s) => s.id === section)?.add}</button>
		{/if}
	</div>

	{#if readOnly}
		<div class="note">
			{#if isView}Views show their columns here; change a view with SQL.{:else}Read-only connection: structure changes are off.{/if}
		</div>
	{/if}

	<div class="body">
		{#if error}
			<div class="pad"><div class="error-box" role="alert">{error}</div></div>
		{:else if !draft}
			<div class="pad muted">Loading…</div>
		{:else if section === 'fields'}
			<table>
				<thead>
					<tr><th>Name</th><th>Type</th><th>Nullable</th><th>Default</th><th>Comment</th><th>Primary</th><th>Extra</th><th></th></tr>
				</thead>
				<tbody>
					{#each shown(draft.fields) as { r, i } (i)}
						<tr class={rowState('fields', r)}>
							<td><input class="cell mono" bind:value={r.cur.name} disabled={readOnly || r.deleted} aria-label="Name" /></td>
							<td><input class="cell mono" bind:value={r.cur.type} disabled={readOnly || r.deleted} aria-label="Type" placeholder="type" /></td>
							<td class="center"><input type="checkbox" bind:checked={r.cur.nullable} disabled={readOnly || r.deleted} aria-label="Nullable" /></td>
							<td><input class="cell mono" bind:value={r.cur.default} disabled={readOnly || r.deleted} aria-label="Default" placeholder="(none)" /></td>
							<td><input class="cell" bind:value={r.cur.comment} disabled={readOnly || r.deleted} aria-label="Comment" placeholder="(none)" /></td>
							<td class="center">{#if r.cur.primary}<span title="Primary key"><Icon name="key" size={14} /></span>{/if}</td>
							<td class="muted mono small" title={r.cur.collation ? `collation ${r.cur.collation}` : ''}>{r.cur.extra}</td>
							<td class="center">{@render del(draft.fields, i, r)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{:else if section === 'indexes'}
			<table>
				<thead><tr><th>Name</th><th>Columns</th><th>Unique</th><th>Primary</th><th>Method</th><th>Definition</th><th></th></tr></thead>
				<tbody>
					{#each shown(draft.indexes) as { r, i } (i)}
						<tr class={rowState('indexes', r)}>
							<td><input class="cell mono" bind:value={r.cur.name} disabled={readOnly || r.deleted} aria-label="Name" placeholder="(generated)" /></td>
							<td>
								<input
									class="cell mono"
									value={joinList(r.cur.fields)}
									onchange={(e) => (r.cur.fields = splitList(e.currentTarget.value))}
									disabled={readOnly || r.deleted}
									aria-label="Columns"
									placeholder="col1, col2"
								/>
							</td>
							<td class="center"><input type="checkbox" bind:checked={r.cur.unique} disabled={readOnly || r.deleted || r.cur.primary} aria-label="Unique" /></td>
							<td class="center"><input type="checkbox" bind:checked={r.cur.primary} disabled={readOnly || r.deleted} aria-label="Primary" /></td>
							<td><input class="cell mono narrow" bind:value={r.cur.method} disabled={readOnly || r.deleted} aria-label="Method" placeholder="default" /></td>
							<td class="muted mono small def" title={r.cur.definition}>{r.cur.definition}</td>
							<td class="center">{@render del(draft.indexes, i, r)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{:else if section === 'relations'}
			<table>
				<thead><tr><th>Name</th><th>Columns</th><th>References table</th><th>Referenced columns</th><th>On update</th><th>On delete</th><th></th></tr></thead>
				<tbody>
					{#each shown(draft.relations) as { r, i } (i)}
						{@const label = targetLabel(r.cur.target, schemaName)}
						<tr class={rowState('relations', r)}>
							<td><input class="cell mono" bind:value={r.cur.name} disabled={readOnly || r.deleted} aria-label="Name" placeholder="(generated)" /></td>
							<td>
								<input
									class="cell mono"
									value={joinList(r.cur.fields)}
									onchange={(e) => (r.cur.fields = splitList(e.currentTarget.value))}
									disabled={readOnly || r.deleted}
									aria-label="Columns"
								/>
							</td>
							<td>
								<select
									class="cell mono"
									value={label}
									onchange={(e) => (r.cur.target = targetOf(e.currentTarget.value))}
									disabled={readOnly || r.deleted}
									aria-label="References table"
								>
									<option value="" disabled>choose…</option>
									{#if label && !tables.some((t) => t.label === label)}<option value={label}>{label}</option>{/if}
									{#each tables as t (t.label)}<option value={t.label}>{t.label}</option>{/each}
								</select>
							</td>
							<td>
								<input
									class="cell mono"
									value={joinList(r.cur.targetFields)}
									onchange={(e) => (r.cur.targetFields = splitList(e.currentTarget.value))}
									disabled={readOnly || r.deleted}
									aria-label="Referenced columns"
								/>
							</td>
							<td>
								<select class="cell" bind:value={r.cur.onUpdate} disabled={readOnly || r.deleted} aria-label="On update">
									<option value="">default</option>
									{#each structure?.actions ?? [] as a (a)}<option value={a}>{a}</option>{/each}
								</select>
							</td>
							<td>
								<select class="cell" bind:value={r.cur.onDelete} disabled={readOnly || r.deleted} aria-label="On delete">
									<option value="">default</option>
									{#each structure?.actions ?? [] as a (a)}<option value={a}>{a}</option>{/each}
								</select>
							</td>
							<td class="center">{@render del(draft.relations, i, r)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{:else}
			<table>
				<thead><tr><th>Name</th><th>Timing</th><th>Events</th><th>For each</th><th>Condition</th><th>Action</th><th></th></tr></thead>
				<tbody>
					{#each shown(draft.triggers) as { r, i } (i)}
						<tr class={rowState('triggers', r)}>
							<td><input class="cell mono" bind:value={r.cur.name} disabled={readOnly || r.deleted} aria-label="Name" /></td>
							<td>
								<select class="cell" bind:value={r.cur.timing} disabled={readOnly || r.deleted} aria-label="Timing">
									{#each timings as t (t)}<option value={t}>{t}</option>{/each}
								</select>
							</td>
							<td>
								<input
									class="cell mono"
									value={r.cur.events.join(' OR ')}
									onchange={(e) => (r.cur.events = splitList(e.currentTarget.value.replace(/\s+or\s+/gi, ',')).map((x) => x.toUpperCase()))}
									disabled={readOnly || r.deleted}
									aria-label="Events"
									placeholder="INSERT OR UPDATE"
								/>
							</td>
							<td>
								<select class="cell" bind:value={r.cur.forEach} disabled={readOnly || r.deleted} aria-label="For each">
									<option value="ROW">ROW</option>
									<option value="STATEMENT">STATEMENT</option>
								</select>
							</td>
							<td><input class="cell mono" bind:value={r.cur.condition} disabled={readOnly || r.deleted} aria-label="Condition" placeholder="(always)" /></td>
							<td class="wide">
								<input
									class="cell mono"
									bind:value={r.cur.action}
									disabled={readOnly || r.deleted}
									aria-label="Action"
									placeholder="EXECUTE FUNCTION f()"
									title={r.cur.definition}
								/>
							</td>
							<td class="center">{@render del(draft.triggers, i, r)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
		{#if draft && draft[section].length === 0}
			<div class="empty">No {sections.find((s) => s.id === section)?.label.toLowerCase()}</div>
		{/if}
	</div>

	{#if pending > 0}
		<div class="pending">
			<span class="badge staging">{pending} pending</span>
			<span class="muted">Nothing changes until you review and apply.</span>
			<span class="spacer"></span>
			<button class="btn" onclick={() => draft && structure && (draft = draftOf(structure))}><Icon name="undo" size={14} /> Discard</button>
			<button class="btn" onclick={toEditor}><Icon name="query" size={14} /> Open SQL in editor</button>
			<button class="btn primary" onclick={review}>Review and apply…</button>
		</div>
	{/if}
</div>

{#snippet del(list: Row<{ name: string }>[], i: number, r: Row<{ name: string }>)}
	{#if !readOnly}
		<button class="btn ghost icon" title={r.deleted ? 'Keep' : 'Delete'} aria-label="{r.deleted ? 'Keep' : 'Delete'} {r.cur.name}" onclick={() => remove(list, i)}>
			<Icon name={r.deleted ? 'undo' : 'close'} size={14} />
		</button>
	{/if}
{/snippet}

{#if plan}
	<Dialog title="Apply {plan.steps.length} statement{plan.steps.length === 1 ? '' : 's'} to {objectName}" wide onclose={() => (plan = null)}>
		{#if production}<div class="banner" role="alert">PRODUCTION · {tab.profile}</div>{/if}
		<p>
			{#if plan.atomic}
				This exact SQL will run in one transaction: if any statement fails, none take effect.
			{:else}
				This exact SQL will run one statement at a time. This database commits each structure change on its own, so if one
				fails, the ones before it stay applied.
			{/if}
		</p>
		<ol class="steps">
			{#each plan.steps as s, i (i)}
				<li class:danger={s.risk === 'destructive'}>
					<pre class="mono">{s.preview};</pre>
					{#if s.risk === 'destructive'}<span class="why"><Icon name="alert" size={13} /> {s.reason || 'destructive'}</span>{/if}
				</li>
			{/each}
		</ol>
		{#if destructive}
			<div class="warning" role="alert">
				<Icon name="alert" size={18} />
				<div><strong>Some statements remove data or objects.</strong> Columns, indexes, relations, or triggers they drop cannot be recovered.</div>
			</div>
			<label class="type">
				<span>Type <strong class="mono">{objectName}</strong> to confirm</span>
				<input class="input mono" bind:value={typed} autocomplete="off" spellcheck="false" />
			</label>
		{/if}
		{#if production}
			<label class="type">
				<span>Type <strong class="mono">{tab.profile}</strong> to confirm</span>
				<input class="input mono" bind:value={typedProfile} autocomplete="off" spellcheck="false" />
			</label>
		{/if}
		{#if planError}<div class="error-box" role="alert">{planError}</div>{/if}
		{#snippet footer()}
			<button class="btn" onclick={() => (plan = null)}>Cancel</button>
			<button class="btn {destructive ? 'danger' : 'primary'}" disabled={!canApply} onclick={apply}>{applying ? 'Applying…' : 'Apply'}</button>
		{/snippet}
	</Dialog>
{/if}

<style>
	.structure {
		display: flex;
		flex-direction: column;
		height: 100%;
		min-height: 0;
		background: var(--surface);
	}
	.tabs {
		display: flex;
		justify-content: center;
		gap: 4px;
		border-bottom: 1px solid var(--border);
		flex: none;
	}
	.tabs button {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 8px 14px;
		border: 0;
		border-bottom: 2px solid transparent;
		background: none;
		color: var(--muted);
		cursor: pointer;
	}
	.tabs button.on {
		color: var(--text);
		border-bottom-color: var(--lapis);
		font-weight: 600;
	}
	.count {
		font-size: 11px;
		color: var(--muted);
		font-weight: 400;
	}
	.toolbar {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 12px;
		flex: none;
	}
	.search {
		display: flex;
		align-items: center;
		gap: 6px;
		color: var(--muted);
		flex: 0 1 320px;
	}
	.search .input {
		flex: 1;
		height: 28px;
	}
	.spacer {
		flex: 1;
	}
	.note {
		padding: 6px 12px;
		color: var(--muted);
		background: var(--surface-2);
		border-top: 1px solid var(--border);
		border-bottom: 1px solid var(--border);
	}
	.body {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 0 12px 12px;
	}
	.pad {
		padding: 12px 0;
	}
	table {
		width: 100%;
		border-collapse: collapse;
	}
	th {
		position: sticky;
		top: 0;
		z-index: 1;
		background: var(--surface);
		text-align: left;
		font-weight: 600;
		font-size: 12px;
		color: var(--muted);
		padding: 6px 6px;
		border-bottom: 1px solid var(--border);
		white-space: nowrap;
	}
	td {
		padding: 2px 4px;
		border-bottom: 1px solid var(--border);
		height: var(--row);
	}
	td.center {
		text-align: center;
		width: 1%;
	}
	td.wide {
		width: 30%;
	}
	td.small {
		font-size: 11px;
	}
	td.def {
		max-width: 280px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.cell {
		width: 100%;
		min-width: 80px;
		height: 26px;
		padding: 0 6px;
		border: 1px solid transparent;
		border-radius: 3px;
		background: transparent;
	}
	.cell.narrow {
		min-width: 60px;
	}
	.cell:hover:not(:disabled),
	.cell:focus {
		border-color: var(--border);
		background: var(--surface);
		outline: none;
	}
	.cell:focus {
		border-color: var(--lapis);
	}
	.cell:disabled {
		color: inherit;
		opacity: 1;
	}
	tr.new,
	tr.changed {
		background: var(--pending);
	}
	tr.new td:first-child,
	tr.changed td:first-child {
		box-shadow: inset 3px 0 0 var(--warning);
	}
	tr.deleted {
		color: var(--muted);
		text-decoration: line-through;
	}
	tr.deleted .cell {
		text-decoration: line-through;
	}
	tr.deleted td:first-child {
		box-shadow: inset 3px 0 0 var(--danger);
	}
	.empty {
		padding: 24px;
		text-align: center;
		color: var(--muted);
		font-weight: 600;
	}
	.pending {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 12px;
		border-top: 1px solid var(--border);
		background: var(--surface-2);
		flex-wrap: wrap;
	}
	.banner {
		margin: -16px -16px 12px;
		padding: 6px 16px;
		background: #b3261e;
		color: #fff;
		font-weight: 700;
		letter-spacing: 0.05em;
	}
	.steps {
		margin: 8px 0 12px;
		padding-left: 24px;
		max-height: 40vh;
		overflow: auto;
	}
	.steps li {
		margin-bottom: 6px;
	}
	.steps pre {
		margin: 0;
		padding: 6px 8px;
		background: var(--surface-2);
		border-radius: var(--radius);
		white-space: pre-wrap;
		word-break: break-word;
	}
	.steps li.danger pre {
		border-left: 3px solid var(--danger);
	}
	.why {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		margin-top: 2px;
		color: var(--danger);
		font-size: 12px;
	}
	.warning {
		display: flex;
		gap: 10px;
		padding: 10px 12px;
		margin-bottom: 12px;
		border: 1px solid var(--danger);
		border-radius: var(--radius);
		color: var(--danger);
	}
	.warning div {
		color: var(--text);
	}
	.type {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-bottom: 12px;
	}
</style>
