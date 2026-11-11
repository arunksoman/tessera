<script lang="ts">
	import { app } from '#lib/state.svelte.ts';
	let { tile, size = 18 }: { tile: string; size?: number } = $props();
	const t = $derived(app.tile(tile));
</script>

<!-- Colour is never the only signal: the glyph names the database type too. -->
<span class="mark tile-{t?.style.color ?? 'slate'}" style:--size="{size}px" title={t?.name ?? tile} aria-hidden="true">
	{t?.style.glyph ?? '?'}
</span>

<style>
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
