<script lang="ts">
	import { api, ApiError, type Files } from '#lib/api.ts';
	import { baseName, crumbs, formatSize, isAbsolute, isDatabaseFile, join } from '#lib/paths.ts';
	import { onMount, untrack } from 'svelte';
	import Dialog from './Dialog.svelte';
	import Icon from './Icon.svelte';

	// Browses the machine running tess serve, which the browser's own file input cannot do: it
	// never reveals a path. With allowNew, a name not in the folder picks a file to create.
	let {
		title,
		start = '',
		allowNew = false,
		databases = false,
		onpick,
		onclose
	}: {
		title: string;
		start?: string;
		allowNew?: boolean;
		/** Show database files first and offer to hide the rest. */
		databases?: boolean;
		onpick: (path: string, isNew: boolean) => void;
		onclose: () => void;
	} = $props();

	let dir = $state<Files | null>(null);
	let name = $state('');
	let error = $state('');
	let busy = $state(false);
	let onlyDatabases = $state(untrack(() => databases));

	const sep = $derived(dir?.sep ?? '/');
	const entries = $derived(
		(dir?.entries ?? []).filter((e) => e.dir || !onlyDatabases || isDatabaseFile(e.name))
	);
	const hidden = $derived((dir?.entries?.length ?? 0) - entries.length);
	// What the button does with the name as typed.
	const target = $derived.by(() => {
		const n = name.trim();
		if (!n) return { action: 'none' as const };
		const hit = dir?.entries?.find((e) => e.name === n);
		if (hit?.dir) return { action: 'folder' as const };
		if (hit) return { action: 'open' as const };
		if (isAbsolute(n) || n.includes('/') || n.includes('\\')) return { action: 'path' as const };
		return allowNew ? { action: 'create' as const } : { action: 'none' as const };
	});

	// Opens on the folder holding start, with its file name ready.
	onMount(() => {
		go(start, true);
	});

	async function go(path: string, first = false) {
		busy = true;
		error = '';
		try {
			let res = await api.files(path);
			if (res.kind !== 'dir') {
				// A file, or one to create: show its folder, or home when that is gone too.
				if (first) name = baseName(res.path, res.sep);
				res = await api.files(res.kind === 'file' || res.parentExists ? (res.parent ?? '') : '');
			} else if (!first) {
				name = '';
			}
			dir = res;
		} catch (e) {
			error = e instanceof ApiError ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	/** Picks the typed name, or follows it when it is a folder or a path. */
	async function submit(e?: SubmitEvent) {
		e?.preventDefault();
		if (!dir) return;
		const n = name.trim();
		if (!n) return;
		const full = isAbsolute(n) ? n : join(dir.path, n, sep);
		try {
			const res = await api.files(full);
			if (res.kind === 'dir') return go(res.path);
			if (res.kind === 'file') return onpick(res.path, false);
			if (!allowNew) {
				error = `There is no file named ${baseName(res.path, res.sep)}.`;
			} else if (!res.parentExists) {
				error = `The folder ${res.parent} does not exist.`;
			} else {
				onpick(res.path, true);
			}
		} catch (err) {
			error = err instanceof ApiError ? err.message : String(err);
		}
	}

	const label = $derived(
		{ none: 'Open', open: 'Open', folder: 'Open folder', path: 'Go', create: 'Create' }[target.action]
	);
</script>

<Dialog {title} wide {onclose}>
	<form class="picker" onsubmit={submit}>
		<nav class="places" aria-label="Places">
			{#if dir?.home}
				<button type="button" class="btn ghost" onclick={() => go(dir!.home)}><Icon name="home" /> Home</button>
			{/if}
			{#each dir?.roots ?? [] as r (r)}
				<button type="button" class="btn ghost" onclick={() => go(r)}><Icon name="drive" /> {r}</button>
			{/each}
		</nav>

		<div class="where">
			<button type="button" class="btn ghost icon" aria-label="Up one folder" title="Up one folder"
				disabled={!dir?.parent} onclick={() => dir?.parent && go(dir.parent)}><Icon name="arrowUp" /></button>
			<ol class="crumbs" aria-label="Folder">
				{#each dir ? crumbs(dir.path, sep) : [] as c, i (c.path)}
					<li>
						{#if i > 0}<span class="slash" aria-hidden="true">›</span>{/if}
						<button type="button" class="crumb" aria-current={c.path === dir?.path ? 'location' : undefined}
							onclick={() => go(c.path)}>{c.label}</button>
					</li>
				{/each}
			</ol>
		</div>

		<div class="list" role="listbox" aria-label="Files" aria-busy={busy}>
			{#if dir?.denied}
				<p class="empty">This folder cannot be read: {dir.denied}</p>
			{:else if dir && entries.length === 0}
				<p class="empty">{hidden ? 'No database files here.' : 'This folder is empty.'}</p>
			{/if}
			{#each entries as e (e.name)}
				{@const db = !e.dir && isDatabaseFile(e.name)}
				<button type="button" role="option" class={['entry', { db }]} aria-selected={!e.dir && e.name === name.trim()}
					onclick={() => (e.dir ? go(join(dir!.path, e.name, sep)) : (name = e.name))}
					ondblclick={() => !e.dir && onpick(join(dir!.path, e.name, sep), false)}>
					<Icon name={e.dir ? 'folder' : db ? 'database' : 'file'} />
					<span class="name">{e.name}</span>
					{#if !e.dir}<span class="size">{formatSize(e.size ?? 0)}</span>{/if}
				</button>
			{/each}
			{#if dir?.truncated}<p class="empty">Only the first {dir.entries?.length} entries are shown.</p>{/if}
		</div>

		<div class="bottom">
			<label for="picker-name">{allowNew ? 'File name, existing or new' : 'File name'}</label>
			<div class="row">
				<input id="picker-name" class="input" bind:value={name} autocomplete="off" spellcheck="false"
					placeholder={allowNew ? 'Pick a file, or type a new name such as app.db' : 'Pick a file'} />
				{#if databases}
					<label class="check"><input type="checkbox" bind:checked={onlyDatabases} /> Database files only</label>
				{/if}
			</div>
			<p class="hint" aria-live="polite">
				{#if error}
					<span class="bad">{error}</span>
				{:else if target.action === 'create'}
					<Icon name="filePlus" size={14} /><span>Creates <strong>{name.trim()}</strong> in this folder when you connect.</span>
				{:else if target.action === 'open'}
					<Icon name="check" size={14} /> Opens the existing file.
				{:else if hidden}
					{hidden} other {hidden === 1 ? 'file is' : 'files are'} hidden.
				{/if}
			</p>
		</div>
	</form>

	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="button" disabled={busy || target.action === 'none'} onclick={() => submit()}>
			{#if target.action === 'create'}<Icon name="filePlus" />{/if}{label}
		</button>
	{/snippet}
</Dialog>

<style>
	.picker {
		display: flex;
		flex-direction: column;
		gap: 10px;
		min-height: 0;
	}
	.places {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.places .btn {
		gap: 6px;
	}
	.where {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 4px;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface-2);
	}
	.crumbs {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		min-width: 0;
		margin: 0;
		padding: 0;
		list-style: none;
		font-family: var(--font-mono);
		font-size: 12px;
	}
	.crumbs li {
		display: flex;
		align-items: center;
	}
	.slash {
		color: var(--muted);
		padding: 0 2px;
	}
	.crumb {
		padding: 2px 4px;
		border: 0;
		border-radius: var(--radius);
		background: none;
		color: var(--text);
		cursor: pointer;
		font: inherit;
	}
	.crumb:hover {
		background: var(--hover);
	}
	.crumb[aria-current] {
		font-weight: 600;
	}
	.list {
		height: min(320px, 45vh);
		overflow: auto;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
	}
	.entry {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		height: var(--row);
		padding: 0 10px;
		border: 0;
		background: none;
		color: var(--text);
		cursor: pointer;
		font: inherit;
		text-align: left;
	}
	.entry:hover {
		background: var(--hover);
	}
	.entry[aria-selected='true'] {
		background: var(--selected);
	}
	.entry :global(.icon) {
		flex: none;
		color: var(--muted);
	}
	.entry.db :global(.icon) {
		color: var(--verdigris);
	}
	.name {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.size {
		color: var(--muted);
		font-size: 12px;
		font-variant-numeric: tabular-nums;
	}
	.empty {
		margin: 0;
		padding: 12px;
		color: var(--muted);
	}
	.bottom label[for] {
		color: var(--muted);
		font-size: 12px;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 12px;
		margin-top: 4px;
	}
	.row .input {
		flex: 1;
		min-width: 0;
	}
	.check {
		display: flex;
		align-items: center;
		gap: 6px;
		white-space: nowrap;
		font-size: 13px;
	}
	.hint {
		display: flex;
		align-items: center;
		gap: 6px;
		min-height: 18px;
		margin: 6px 0 0;
		color: var(--muted);
		font-size: 12px;
	}
	.bad {
		color: var(--danger);
	}
	.btn.primary {
		gap: 6px;
	}
	@media (max-width: 480px) {
		.row {
			flex-direction: column;
			align-items: stretch;
		}
	}
</style>
