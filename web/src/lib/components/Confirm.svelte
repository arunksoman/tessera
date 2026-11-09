<script lang="ts">
	import type { Snippet } from 'svelte';
	import Dialog from './Dialog.svelte';

	// Confirms risky work. On a production connection the user types its name, so a habit
	// of clicking through cannot reach production.
	let {
		title,
		profile,
		production,
		action = 'Run',
		busy = false,
		onconfirm,
		oncancel,
		children
	}: {
		title: string;
		profile: string;
		production: boolean;
		action?: string;
		busy?: boolean;
		onconfirm: () => void;
		oncancel: () => void;
		children: Snippet;
	} = $props();

	let typed = $state('');
	const ok = $derived(!busy && (!production || typed === profile));
</script>

<Dialog {title} wide onclose={oncancel}>
	{#if production}
		<div class="banner" role="alert">PRODUCTION · {profile}</div>
	{/if}
	{@render children()}
	{#if production}
		<label class="type">
			<span>Type <strong class="mono">{profile}</strong> to confirm</span>
			<input class="input mono" bind:value={typed} autocomplete="off" spellcheck="false" />
		</label>
	{/if}
	{#snippet footer()}
		<button class="btn" onclick={oncancel}>Cancel</button>
		<button class="btn danger" disabled={!ok} onclick={onconfirm}>{action}</button>
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
	.type {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-top: 14px;
	}
</style>
