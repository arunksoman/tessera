<script lang="ts">
	import { api, ApiError, type Profile, type Saved } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import { untrack } from 'svelte';
	import Dialog from './Dialog.svelte';

	let { editing, onclose }: { editing?: Profile; onclose: () => void } = $props();
	// The form starts from the profile as it was when opened.
	const initial = untrack(() => editing);

	// The form is rendered from the tile's own Form, so a new database type needs no UI code.
	let name = $state(initial?.name ?? '');
	let tileID = $state(initial?.tile ?? app.tiles[0]?.id ?? '');
	let env = $state(initial?.env ?? '');
	let readOnly = $state(initial?.readOnly ?? false);
	let noHistory = $state(initial?.noHistory ?? false);
	let values = $state<Record<string, string>>({});
	let secrets = $state<Record<string, string>>({});
	let busy = $state(false);
	let error = $state('');
	let saved = $state<Saved | null>(null);

	const tile = $derived(app.tile(tileID));

	// Fill defaults for the chosen type, keeping what the profile already has.
	$effect(() => {
		const t = tile;
		if (!t) return;
		const next: Record<string, string> = {};
		for (const f of t.form) {
			if (f.type === 'secret') continue;
			next[f.name] = editing && editing.tile === t.id ? (editing.values[f.name] ?? '') : (f.default ?? '');
		}
		values = next;
		secrets = {};
	});

	async function save(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		saved = null;
		try {
			const res = await api.saveProfile(name.trim(), {
				from: editing?.name,
				tile: tileID,
				env,
				readOnly,
				noHistory,
				values: Object.fromEntries(Object.entries(values).filter(([, v]) => v.trim() !== '')),
				secrets: Object.fromEntries(Object.entries(secrets).filter(([, v]) => v !== ''))
			});
			if (editing && editing.name !== res.name) app.closeProfileTabs(editing.name);
			await app.reloadProfiles();
			if (res.connected) {
				app.notify(`Connected to ${res.name}`);
				onclose();
			} else {
				saved = res;
			}
		} catch (err) {
			error = err instanceof ApiError ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<Dialog title={editing ? `Edit ${editing.name}` : 'New connection'} {onclose}>
	<form id="conn-form" onsubmit={save}>
		<div class="grid">
			<label for="f-type">Database type</label>
			<select id="f-type" class="input" bind:value={tileID}>
				{#each app.tiles as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
			</select>

			<label for="f-name">Name</label>
			<input id="f-name" class="input" bind:value={name} required pattern="[A-Za-z0-9][A-Za-z0-9_.\-]*"
				title="Letters, digits, '_', '.', and '-'" placeholder="e.g. app-dev" autocomplete="off" />

			{#each tile?.form ?? [] as f (f.name)}
				<label for="f-{f.name}">{f.label}{#if f.required}<span class="req" aria-hidden="true">*</span>{/if}</label>
				{#if f.type === 'secret'}
					<input id="f-{f.name}" class="input" type="password" bind:value={secrets[f.name]} autocomplete="new-password"
						placeholder={editing?.tile === tileID && editing.secrets[f.name] ? 'Saved; leave blank to keep' : ''} />
				{:else if f.type === 'enum'}
					<select id="f-{f.name}" class="input" bind:value={values[f.name]}>
						{#if !f.required}<option value="">(default)</option>{/if}
						{#each f.options ?? [] as o (o)}<option value={o}>{o}</option>{/each}
					</select>
				{:else if f.type === 'bool'}
					<input id="f-{f.name}" type="checkbox" checked={values[f.name] === 'true'}
						onchange={(e) => (values[f.name] = e.currentTarget.checked ? 'true' : 'false')} />
				{:else}
					<input id="f-{f.name}" class="input" bind:value={values[f.name]} required={f.required}
						inputmode={f.type === 'int' ? 'numeric' : undefined} pattern={f.type === 'int' ? '[0-9]*' : undefined}
						placeholder={f.type === 'file' ? 'Path on this machine' : ''} autocomplete="off" />
				{/if}
			{/each}

			<label for="f-env">Environment</label>
			<select id="f-env" class="input" bind:value={env}>
				<option value="">None</option>
				<option value="development">Development</option>
				<option value="staging">Staging</option>
				<option value="production">Production: confirm every write</option>
			</select>

			<span></span>
			<label class="check"><input type="checkbox" bind:checked={readOnly} /> Read-only</label>
			<span></span>
			<label class="check"><input type="checkbox" bind:checked={noHistory} /> Keep no query history</label>
		</div>

		{#if error}<p class="error-box" role="alert">{error}</p>{/if}
		{#if saved}
			<div class="error-box" role="alert"><strong>Saved, but could not connect.</strong><br />{saved.error}</div>
		{/if}
		{#each saved?.warnings ?? [] as w (w)}<p class="warn">⚠ {w}</p>{/each}
	</form>

	{#snippet footer()}
		{#if saved}<button class="btn" type="button" onclick={onclose}>Close anyway</button>{/if}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="submit" form="conn-form" disabled={busy}>{busy ? 'Connecting…' : 'Save and connect'}</button>
	{/snippet}
</Dialog>

<style>
	.grid {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 8px 12px;
		align-items: center;
	}
	label {
		color: var(--muted);
	}
	.check {
		display: flex;
		align-items: center;
		gap: 6px;
		color: var(--text);
	}
	.req {
		color: var(--danger);
		margin-left: 2px;
	}
	.error-box {
		margin: 12px 0 0;
	}
	.warn {
		color: var(--warning);
		margin: 8px 0 0;
	}
</style>
