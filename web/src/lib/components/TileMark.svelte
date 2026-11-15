<script lang="ts">
	import { brands, tileBrands } from '#lib/icons/index.ts';
	import { app } from '#lib/state.svelte.ts';
	let { tile, size = 18, all = false }: { tile: string; size?: number; all?: boolean } = $props();
	const t = $derived(app.tile(tile));
	// Built-in tiles show their database's logo; plugins keep the colour square and glyph.
	const logos = $derived((tileBrands[tile] ?? []).slice(0, all ? undefined : 1));
</script>

<!-- Colour is never the only signal: the logo or glyph names the database type too. -->
{#if logos.length}
	<span class="logos tile-{t?.style.color ?? 'slate'}" title={t?.name ?? tile} aria-hidden="true">
		{#each logos as b (b)}
			<svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" focusable="false">
				{#each brands[b] as d, i (i)}<path {d} />{/each}
			</svg>
		{/each}
	</span>
{:else}
	<span class="mark tile-{t?.style.color ?? 'slate'}" style:--size="{size}px" title={t?.name ?? tile} aria-hidden="true">
		{t?.style.glyph ?? '?'}
	</span>
{/if}

<style>
	.logos {
		display: inline-flex;
		gap: 4px;
		flex: none;
		color: var(--tile);
	}
	.logos svg {
		display: block;
	}
	.mark {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex: none;
		width: var(--size);
		height: var(--size);
		border-radius: 3px;
		background: var(--tile);
		color: var(--on-accent);
		font-size: calc(var(--size) * 0.6);
		font-weight: 700;
		line-height: 1;
	}
</style>
