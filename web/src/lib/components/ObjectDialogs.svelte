<script lang="ts">
	import { api, ApiError, type ExportFormat, type ImportFormat, type Plan, type Ref } from '#lib/api.ts';
	import { app, type ObjectDialog } from '#lib/state.svelte.ts';
	import Dialog from './Dialog.svelte';
	import Icon from './Icon.svelte';

	// Export, import, rename, truncate, and drop for one table or view, opened from the tree.
	let { dialog }: { dialog: ObjectDialog } = $props();

	const name = $derived(dialog.ref[dialog.ref.length - 1]?.name ?? '');
	const kind = $derived(dialog.ref[dialog.ref.length - 1]?.kind ?? 'table');
	const profile = $derived(app.profile(dialog.profile));
	const production = $derived(profile?.env === 'production');
	const destructive = $derived(dialog.kind === 'truncate' || dialog.kind === 'drop');

	const close = () => (app.dialog = null);

	// --- export ---
	let format = $state<ExportFormat>('csv');
	let neutralize = $state(true);
	const formats: { id: ExportFormat; label: string; note: string }[] = [
		{ id: 'csv', label: 'CSV', note: 'A header row, then one line per row. NULL is an empty field.' },
		{ id: 'json', label: 'JSON', note: 'One array of objects. Numbers stay exact.' },
		{ id: 'jsonl', label: 'JSON Lines', note: 'One object per line, easy to stream.' },
		{ id: 'sql', label: 'SQL', note: 'INSERT statements, 100 rows each.' }
	];

	function startExport() {
		const a = document.createElement('a');
		a.href = api.exportURL(dialog.profile, dialog.ref, format, neutralize);
		a.download = '';
		document.body.append(a);
		a.click();
		a.remove();
		app.notify(`Exporting ${name}: your browser saves the file as it streams.`);
		close();
	}

	// --- import ---
	let file = $state<File | null>(null);
	let importFormat = $state<ImportFormat>('csv');
	let emptyNull = $state(true);

	function pick(e: Event) {
		file = (e.currentTarget as HTMLInputElement).files?.[0] ?? null;
		const ext = file?.name.split('.').pop()?.toLowerCase();
		if (ext === 'json') importFormat = 'json';
		else if (ext === 'jsonl' || ext === 'ndjson') importFormat = 'jsonl';
		else if (ext === 'csv') importFormat = 'csv';
	}

	function startImport() {
		if (!file) return;
		const f = file;
		const { profile: p, ref } = dialog;
		const table = name;
		close();
		app
			.runJob(`Importing ${f.name} into ${table}`, (signal) =>
				api.importFile(p, ref, f, importFormat, { emptyNull, confirm: true }, signal)
			)
			.then((r) => app.notify(`Imported ${r.rows.toLocaleString()} row${r.rows === 1 ? '' : 's'} into ${table}.`))
			.catch((e) => {
				if ((e as Error).name === 'AbortError') app.notify(`Import into ${table} cancelled; nothing was added.`);
				else app.notify(`Import into ${table} failed; nothing was added. ${(e as Error).message}`, 'error');
			});
	}

	// --- rename, truncate, drop ---
	let newName = $state('');
	let plan = $state<Plan | null>(null);
	let error = $state('');
	let busy = $state(false);
	let typed = $state('');
	let typedProfile = $state('');

	$effect(() => {
		newName = name;
		if (destructive) preview();
	});

	async function preview() {
		error = '';
		plan = null;
		const change = dialog.kind === 'rename' ? { op: 'rename', name: newName.trim() } : { op: dialog.kind };
		try {
			plan = await api.planStructure(dialog.profile, dialog.ref, [change]);
		} catch (e) {
			error = (e as Error).message;
		}
	}

	const canApply = $derived(
		!!plan && !busy && (!destructive || typed === name) && (destructive || !production || typedProfile === dialog.profile)
	);

	/** Closes tabs showing an object that no longer exists under its old name. */
	function closeTabsOf(ref: Ref) {
		const key = JSON.stringify(ref);
		for (const t of app.tabs.filter((t) => t.profile === dialog.profile && t.kind !== 'sql' && JSON.stringify(t.ref) === key)) {
			app.closeTab(t.id);
		}
	}

	async function apply() {
		if (!plan) return;
		busy = true;
		error = '';
		try {
			// This dialog is the confirmation: the name typed for destructive work, and the
			// connection's name on production.
			await api.apply(dialog.profile, plan.id, true);
			const done = { rename: `Renamed ${name} to ${newName.trim()}.`, truncate: `Emptied ${name}.`, drop: `Dropped ${name}.` };
			app.notify(done[dialog.kind as 'rename' | 'truncate' | 'drop']);
			if (dialog.kind !== 'truncate') closeTabsOf(dialog.ref);
			app.refreshTree(dialog.profile);
			close();
		} catch (e) {
			error = e instanceof ApiError && e.code === 'conflict' ? 'The plan expired; preview it again.' : (e as Error).message;
		} finally {
			busy = false;
		}
	}

	const titles = $derived({
		export: `Export ${name}`,
		import: `Import data into ${name}`,
		rename: `Rename ${kind} ${name}`,
		truncate: `Truncate ${name}`,
		drop: `Drop ${kind} ${name}`
	});
</script>

<Dialog title={titles[dialog.kind]} onclose={close} wide={dialog.kind !== 'export'}>
	{#if production && dialog.kind !== 'export'}
		<div class="banner" role="alert">PRODUCTION · {dialog.profile}</div>
	{/if}

	{#if dialog.kind === 'export'}
		<fieldset class="formats">
			<legend>Format</legend>
			{#each formats as f (f.id)}
				<label class="choice">
					<input type="radio" name="format" value={f.id} bind:group={format} />
					<span><strong>{f.label}</strong> <span class="muted">{f.note}</span></span>
				</label>
			{/each}
		</fieldset>
		{#if format === 'csv'}
			<label class="check">
				<input type="checkbox" bind:checked={neutralize} />
				<span>Neutralize formulas, so a spreadsheet cannot run cell text starting with = + - or @</span>
			</label>
		{/if}
		<p class="muted note">
			Every row is exported, read straight from the database as the file downloads, so large tables never load into this page.
			The download continues in the background; your browser shows its progress.
		</p>
	{:else if dialog.kind === 'import'}
		<label class="field">
			<span>File</span>
			<input class="input" type="file" accept=".csv,.json,.jsonl,.ndjson,text/csv,application/json" onchange={pick} />
		</label>
		<label class="field">
			<span>Format</span>
			<select class="input" bind:value={importFormat}>
				<option value="csv">CSV with a header row</option>
				<option value="json">JSON array of objects</option>
				<option value="jsonl">JSON Lines</option>
			</select>
		</label>
		{#if importFormat === 'csv'}
			<label class="check">
				<input type="checkbox" bind:checked={emptyNull} />
				<span>Load empty fields as NULL (fields of non-text columns always are)</span>
			</label>
		{/if}
		<p class="muted note">
			Column names in the header or object keys must match the columns of <strong class="mono">{name}</strong>; columns left out
			get their defaults. Rows are added in one transaction, so if any row fails, none are added. The import runs in the
			background and is listed at the bottom of the window until it ends.
		</p>
		{#if production}
			<label class="field">
				<span>Type <strong class="mono">{dialog.profile}</strong> to confirm</span>
				<input class="input mono" bind:value={typedProfile} autocomplete="off" spellcheck="false" />
			</label>
		{/if}
	{:else}
		{#if dialog.kind === 'rename'}
			<form
				class="rename"
				onsubmit={(e) => {
					e.preventDefault();
					preview();
				}}
			>
				<label class="field grow">
					<span>New name</span>
					<!-- svelte-ignore a11y_autofocus -->
					<input class="input mono" bind:value={newName} autocomplete="off" spellcheck="false" autofocus />
				</label>
				<button class="btn" type="submit" disabled={!newName.trim() || newName.trim() === name}>Preview SQL</button>
			</form>
		{:else}
			<div class="warning" role="alert">
				<Icon name="alert" size={18} />
				<div>
					{#if dialog.kind === 'truncate'}
						<strong>This permanently deletes every row in {name}.</strong>
						The table and its structure stay. This cannot be undone.
					{:else}
						<strong>This permanently deletes the {kind} {name}</strong>{kind === 'table'
							? ', with all its rows, indexes, and triggers'
							: ''}. Objects that depend on it may stop working. This cannot be undone.
					{/if}
				</div>
			</div>
		{/if}

		{#if plan}
			<p>
				This exact SQL will run{plan.atomic ? ' in one transaction' : ''}:
			</p>
			<pre class="preview mono">{plan.steps.map((s) => s.preview + ';').join('\n')}</pre>
		{/if}

		{#if destructive}
			<label class="field">
				<span>Type <strong class="mono">{name}</strong> to confirm</span>
				<input class="input mono" bind:value={typed} autocomplete="off" spellcheck="false" />
			</label>
		{:else if production && plan}
			<label class="field">
				<span>Type <strong class="mono">{dialog.profile}</strong> to confirm</span>
				<input class="input mono" bind:value={typedProfile} autocomplete="off" spellcheck="false" />
			</label>
		{/if}
		{#if error}<div class="error-box" role="alert">{error}</div>{/if}
	{/if}

	{#snippet footer()}
		<button class="btn" onclick={close}>Cancel</button>
		{#if dialog.kind === 'export'}
			<button class="btn primary" onclick={startExport}><Icon name="download" size={14} /> Export</button>
		{:else if dialog.kind === 'import'}
			<button class="btn primary" disabled={!file || (production && typedProfile !== dialog.profile)} onclick={startImport}>
				<Icon name="upload" size={14} /> Import
			</button>
		{:else}
			<button class="btn {destructive ? 'danger' : 'primary'}" disabled={!canApply} onclick={apply}>
				{busy ? 'Working…' : { rename: 'Rename', truncate: 'Truncate', drop: 'Drop' }[dialog.kind as 'rename' | 'truncate' | 'drop']}
			</button>
		{/if}
	{/snippet}
</Dialog>

<style>
	.banner {
		margin: -16px -16px 12px;
		padding: 6px 16px;
		background: #b3261e;
		color: #fff;
		font-weight: 700;
		letter-spacing: 0.05em;
	}
	.formats {
		border: 0;
		margin: 0 0 12px;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	legend {
		padding: 0;
		margin-bottom: 6px;
		font-weight: 600;
	}
	.choice,
	.check {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		cursor: pointer;
	}
	.choice input,
	.check input {
		margin-top: 3px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		margin: 0 0 12px;
	}
	.grow {
		flex: 1;
		margin: 0;
	}
	.rename {
		display: flex;
		align-items: flex-end;
		gap: 8px;
		margin-bottom: 12px;
	}
	.note {
		margin: 12px 0 0;
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
	.preview {
		margin: 8px 0 12px;
		padding: 10px;
		background: var(--surface-2);
		border-radius: var(--radius);
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 40vh;
		overflow: auto;
	}
</style>
