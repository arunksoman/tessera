<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		title,
		onclose,
		wide = false,
		children,
		footer
	}: { title: string; onclose: () => void; wide?: boolean; children: Snippet; footer?: Snippet } = $props();

	let el: HTMLDialogElement;
	$effect(() => {
		el.showModal();
		return () => el.close();
	});
</script>

<!-- A native modal dialog: focus stays inside and Esc closes it. -->
<dialog
	bind:this={el}
	class:wide
	aria-label={title}
	oncancel={(e) => {
		e.preventDefault();
		onclose();
	}}
>
	<header>
		<h2>{title}</h2>
		<button class="btn ghost icon" aria-label="Close" onclick={onclose}>✕</button>
	</header>
	<div class="body">{@render children()}</div>
	{#if footer}<footer>{@render footer()}</footer>{/if}
</dialog>

<style>
	dialog {
		width: min(520px, calc(100vw - 32px));
		max-height: calc(100vh - 64px);
		padding: 0;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--surface);
		color: var(--text);
		display: flex;
		flex-direction: column;
	}
	dialog:not([open]) {
		display: none;
	}
	dialog.wide {
		width: min(860px, calc(100vw - 32px));
	}
	dialog::backdrop {
		background: rgb(0 0 0 / 0.4);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 10px 12px 10px 16px;
		border-bottom: 1px solid var(--border);
	}
	h2 {
		margin: 0;
		font-size: 15px;
		font-weight: 600;
	}
	.body {
		padding: 16px;
		overflow: auto;
	}
	footer {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		padding: 10px 16px;
		border-top: 1px solid var(--border);
	}
</style>
