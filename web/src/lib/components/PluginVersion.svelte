<script lang="ts">
	import type { Tile } from '#lib/api.ts';
	let { tile }: { tile: Tile | undefined } = $props();
</script>

<!-- Built-in tiles ship with tess, so only plugins show a version and stability. -->
{#if tile?.version || tile?.stability}
	<span class="plugin">
		plugin{#if tile.version}&nbsp;{tile.version}{/if}
		{#if tile.stability}<span class="badge {tile.stability}" title="Plugin stability: {tile.stability}">{tile.stability}</span>{/if}
	</span>
{/if}

<style>
	.plugin {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		color: var(--muted);
		font-size: 12px;
		font-weight: 400;
	}
	.alpha {
		background: var(--warning);
		color: var(--on-accent);
	}
	.beta {
		border: 1px solid var(--lapis);
		color: var(--lapis);
	}
	.stable {
		border: 1px solid var(--border);
		color: var(--muted);
	}
</style>
