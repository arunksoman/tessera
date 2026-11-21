<script lang="ts">
	import { tidy, type MenuItem } from '#lib/menu.ts';
	import { onMount } from 'svelte';
	import Icon from './Icon.svelte';

	// A context menu at a point, following the ARIA menu pattern: arrows move, Enter runs,
	// Escape or a click elsewhere closes, and focus goes back where it was.
	let { x, y, items, label = 'Actions', onclose }: { x: number; y: number; items: MenuItem[]; label?: string; onclose: () => void } =
		$props();

	let menu: HTMLDivElement;
	let width = $state(0);
	let height = $state(0);
	let innerWidth = $state(0);
	let innerHeight = $state(0);
	const shown = $derived(tidy(items));
	// Keep the whole menu on screen, flipping above or left of the point when it would overflow.
	const left = $derived(x + width > innerWidth - 4 ? Math.max(4, x - width) : x);
	const top = $derived(y + height > innerHeight - 4 ? Math.max(4, y - height) : y);

	let returnFocus: HTMLElement | null = null;

	onMount(() => {
		returnFocus = document.activeElement as HTMLElement | null;
		buttons()[0]?.focus();
		return () => returnFocus?.focus();
	});

	const buttons = () => [...menu.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')];

	function run(it: Exclude<MenuItem, 'separator'>) {
		onclose();
		it.action();
	}

	function keydown(e: KeyboardEvent) {
		const list = buttons();
		const i = list.indexOf(document.activeElement as HTMLButtonElement);
		const move: Record<string, number> = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: list.length - 1 };
		if (e.key in move) {
			e.preventDefault();
			list[(move[e.key] + list.length) % list.length]?.focus();
		} else if (e.key === 'Escape' || e.key === 'Tab') {
			e.preventDefault();
			e.stopPropagation();
			onclose();
		}
	}

	function outside(e: Event) {
		if (!menu.contains(e.target as Node)) onclose();
	}
</script>

<svelte:window bind:innerWidth bind:innerHeight onpointerdown={outside} onwheel={outside} onresize={onclose} onblur={onclose} />

<div
	class="menu"
	role="menu"
	aria-label={label}
	tabindex="-1"
	bind:this={menu}
	bind:clientWidth={width}
	bind:clientHeight={height}
	style:left="{left}px"
	style:top="{top}px"
	onkeydown={keydown}
	oncontextmenu={(e) => e.preventDefault()}
>
	{#each shown as it, i (i)}
		{#if it === 'separator'}
			<div class="sep" role="separator"></div>
		{:else}
			<button role="menuitem" disabled={it.disabled} onclick={() => run(it)}>
				<span class="icon">{#if it.icon}<Icon name={it.icon} size={14} />{/if}</span>
				<span class="label">{it.label}</span>
				{#if it.hint}<span class="hint">{it.hint}</span>{/if}
			</button>
		{/if}
	{/each}
</div>

<style>
	.menu {
		position: fixed;
		z-index: 50;
		min-width: 200px;
		max-width: min(420px, calc(100vw - 8px));
		padding: 4px;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		box-shadow: 0 6px 24px rgb(0 0 0 / 0.18);
		font-family: var(--font-ui);
		font-size: 13px;
	}
	.menu:focus {
		outline: none;
	}
	button {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		height: 28px;
		padding: 0 8px;
		border: 0;
		border-radius: 3px;
		background: none;
		text-align: left;
		cursor: pointer;
	}
	button:hover:not(:disabled),
	button:focus-visible {
		background: var(--selected);
		outline: none;
	}
	button:disabled {
		color: var(--muted);
		cursor: default;
	}
	.icon {
		display: inline-flex;
		justify-content: center;
		width: 14px;
		flex: none;
		color: var(--muted);
	}
	.label {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.hint {
		flex: none;
		color: var(--muted);
		font-size: 11px;
	}
	.sep {
		height: 1px;
		margin: 4px 6px;
		background: var(--border);
	}
</style>
