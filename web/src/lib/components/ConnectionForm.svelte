<script lang="ts">
	import { api, ApiError, type FormField, type Profile, type Saved } from '#lib/api.ts';
	import { envOf, envs } from '#lib/connections.ts';
	import { app } from '#lib/state.svelte.ts';
	import { untrack } from 'svelte';
	import Dialog from './Dialog.svelte';
	import Icon from './Icon.svelte';
	import TileMark from './TileMark.svelte';

	let { editing, onclose }: { editing?: Profile; onclose: () => void } = $props();
	// The form starts from the profile as it was when opened.
	const initial = untrack(() => editing);

	// The form is rendered from the tile's own Form, so a new database type needs no UI code.
	// A new connection starts by picking the type, unless there is only one.
	let step = $state<'pick' | 'form'>(initial || app.tiles.length < 2 ? 'form' : 'pick');
	let name = $state(initial?.name ?? '');
	let tileID = $state(initial?.tile ?? app.tiles[0]?.id ?? '');
	let env = $state(initial?.env ?? '');
	let readOnly = $state(initial?.readOnly ?? false);
	let noHistory = $state(initial?.noHistory ?? false);
	let values = $state<Record<string, string>>({});
	let secrets = $state<Record<string, string>>({});
	let reveal = $state<Record<string, boolean>>({});
	let busy = $state(false);
	let error = $state('');
	let saved = $state<Saved | null>(null);

	const tile = $derived(app.tile(tileID));
	// Keep a tag this build does not offer, such as one set from the command line.
	const choices = $derived(envs.some((e) => e.value === initial?.env) || !initial?.env ? envs : [...envs, envOf(initial.env)]);

	// Enums and files are TLS modes and certificates for the built-in tiles: they go in a
	// collapsed section, so the common fields come first.
	const advanced = (f: FormField) => f.type === 'enum' || f.type === 'file';
	const basic = $derived(nextToHost((tile?.form ?? []).filter((f) => !advanced(f))));
	const extra = $derived((tile?.form ?? []).filter(advanced));
	const extraTitle = $derived(extra.every((f) => /ssl|tls/i.test(f.name)) ? 'TLS and certificates' : 'Advanced');
	const extraSummary = $derived(
		extra
			.filter((f) => f.type === 'enum')
			.map((f) => values[f.name] || 'default')
			.join(', ')
	);
	// Pairs that read well side by side; anything else takes the full row. The port moves up
	// beside the host, since MySQL lists the socket between them.
	function nextToHost(fields: FormField[]): FormField[] {
		const host = fields.findIndex((f) => f.name === 'host');
		const port = fields.findIndex((f) => f.name === 'port');
		if (host < 0 || port < 0) return fields;
		const rest = fields.filter((_, i) => i !== port);
		rest.splice(rest.findIndex((f) => f.name === 'host') + 1, 0, fields[port]);
		return rest;
	}
	const span: Record<string, string> = { host: 'wide', port: 'narrow', user: 'half', password: 'half' };

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

	function pick(id: string) {
		tileID = id;
		step = 'form';
	}

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
			if (editing && editing.name !== res.name) {
				app.closeProfileTabs(editing.name);
				app.renamed(editing.name, res.name);
			}
			await app.reloadProfiles();
			if (res.connected) {
				app.used(res.name);
				app.expanded[res.name] = true;
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

{#snippet field(f: FormField)}
	<div class="field {span[f.name] ?? 'full'}">
		<label for="f-{f.name}">{f.label}{#if f.required}<span class="req" aria-hidden="true">*</span>{/if}</label>
		{#if f.type === 'secret'}
			<div class="secret">
				<input id="f-{f.name}" class="input" type={reveal[f.name] ? 'text' : 'password'} bind:value={secrets[f.name]}
					autocomplete="new-password" placeholder={editing?.tile === tileID && editing.secrets[f.name] ? 'Saved; leave blank to keep' : ''} />
				<button type="button" class="btn ghost icon" aria-label={reveal[f.name] ? 'Hide' : 'Show'} aria-pressed={!!reveal[f.name]}
					onclick={() => (reveal[f.name] = !reveal[f.name])}><Icon name={reveal[f.name] ? 'eyeOff' : 'eye'} /></button>
			</div>
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
	</div>
{/snippet}

{#if step === 'pick'}
	<Dialog title="New connection" {onclose}>
		<p class="lead">Choose a database type.</p>
		<div class="types">
			{#each app.tiles as t (t.id)}
				<button type="button" class="type" onclick={() => pick(t.id)}>
					<TileMark tile={t.id} size={32} all />
					<span>{t.name}</span>
				</button>
			{/each}
		</div>
		{#snippet footer()}
			<button class="btn" type="button" onclick={onclose}>Cancel</button>
		{/snippet}
	</Dialog>
{:else}
	<Dialog title={editing ? `Edit ${editing.name}` : 'New connection'} {onclose}>
		<form id="conn-form" onsubmit={save}>
			<div class="kind">
				<TileMark tile={tileID} size={24} all />
				<strong>{tile?.name ?? tileID}</strong>
				{#if !editing && app.tiles.length > 1}
					<button type="button" class="btn ghost change" onclick={() => (step = 'pick')}>Change</button>
				{/if}
			</div>

			<div class="fields">
				{#each basic as f (f.name)}{@render field(f)}{/each}
			</div>

			{#if extra.length}
				<details>
					<summary>{extraTitle}{#if extraSummary}<span class="muted mode">· {extraSummary}</span>{/if}</summary>
					<div class="fields">
						{#each extra as f (f.name)}{@render field(f)}{/each}
					</div>
				</details>
			{/if}

			<h3>Save as</h3>
			<div class="fields">
				<div class="field full">
					<label for="f-name">Connection name<span class="req" aria-hidden="true">*</span></label>
					<input id="f-name" class="input" bind:value={name} required pattern="[A-Za-z0-9][A-Za-z0-9_.\-]*"
						title="Letters, digits, '_', '.', and '-'" placeholder="e.g. app-dev" autocomplete="off" />
				</div>
			</div>

			<fieldset class="envs">
				<legend>Environment colour</legend>
				{#each choices as e (e.value)}
					<label class="env-choice env-{e.color}" title={e.note}>
						<input type="radio" name="env" value={e.value} bind:group={env} />
						<span class="swatch" aria-hidden="true"></span>
						{e.label}
					</label>
				{/each}
			</fieldset>
			<p class="note muted">{envOf(env).note}</p>

			<div class="options">
				<label class="check"><input type="checkbox" bind:checked={readOnly} /> Read-only</label>
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
{/if}

<style>
	.lead {
		margin: 0 0 12px;
		color: var(--muted);
	}
	.types {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
		gap: 10px;
	}
	.type {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 10px;
		padding: 18px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--surface);
		cursor: pointer;
		font-weight: 500;
	}
	.type:hover {
		background: var(--hover);
		border-color: var(--lapis);
	}
	.kind {
		display: flex;
		align-items: center;
		gap: 10px;
		padding-bottom: 12px;
		margin-bottom: 12px;
		border-bottom: 1px solid var(--border);
	}
	.change {
		margin-left: auto;
		color: var(--lapis);
	}
	.fields {
		display: grid;
		grid-template-columns: repeat(6, 1fr);
		gap: 10px 12px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
		grid-column: span 6;
	}
	.field.wide {
		grid-column: span 4;
	}
	.field.narrow {
		grid-column: span 2;
	}
	.field.half {
		grid-column: span 3;
	}
	@media (max-width: 480px) {
		.field.wide,
		.field.narrow,
		.field.half {
			grid-column: span 6;
		}
	}
	label {
		color: var(--muted);
		font-size: 12px;
	}
	.secret {
		display: flex;
		position: relative;
	}
	.secret .input {
		flex: 1;
		padding-right: 34px;
	}
	.secret .btn {
		position: absolute;
		right: 1px;
		top: 1px;
		color: var(--muted);
	}
	details {
		margin-top: 14px;
		padding: 8px 12px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--surface-2);
	}
	details[open] > .fields {
		margin: 8px 0 4px;
	}
	summary {
		cursor: pointer;
		font-weight: 500;
	}
	.mode {
		margin-left: 6px;
		font-weight: 400;
	}
	h3 {
		margin: 18px 0 8px;
		font-size: 13px;
		font-weight: 600;
	}
	.envs {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin: 12px 0 0;
		padding: 0;
		border: 0;
	}
	legend {
		padding: 0;
		margin-bottom: 6px;
		color: var(--muted);
		font-size: 12px;
	}
	/* Each choice is a radio with its colour and its name, so colour is never the only signal. */
	.env-choice {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		height: 28px;
		padding: 0 10px 0 8px;
		border: 1px solid var(--border);
		border-radius: 14px;
		cursor: pointer;
		color: var(--text);
		font-size: 13px;
	}
	.env-choice input {
		position: absolute;
		opacity: 0;
		pointer-events: none;
	}
	.swatch {
		width: 12px;
		height: 12px;
		border-radius: 50%;
		background: var(--env);
		border: 1px solid var(--env);
	}
	.env-none .swatch {
		border-color: var(--muted);
	}
	.env-choice:has(input:checked) {
		border-color: var(--lapis);
		background: var(--selected);
		font-weight: 600;
	}
	.env-choice:has(input:focus-visible) {
		outline: 2px solid var(--lapis);
		outline-offset: 1px;
	}
	.note {
		margin: 6px 0 0;
		font-size: 12px;
		min-height: 1.45em;
	}
	.options {
		display: flex;
		flex-wrap: wrap;
		gap: 6px 18px;
		margin-top: 12px;
	}
	.check {
		display: flex;
		align-items: center;
		gap: 6px;
		color: var(--text);
		font-size: 13px;
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
