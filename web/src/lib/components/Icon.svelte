<script lang="ts">
	import { ui, type IconName } from '#lib/icons/index.ts';
	let { name, size = 16 }: { name: IconName; size?: number } = $props();
</script>

<!-- Decorative: the button or label around it carries the text. -->
<svg
	class="icon"
	width={size}
	height={size}
	viewBox="0 0 24 24"
	fill="none"
	stroke="currentColor"
	stroke-width="2"
	stroke-linecap="round"
	stroke-linejoin="round"
	aria-hidden="true"
	focusable="false"
>
	{#each ui[name] as d, i (i)}<path {d} />{/each}
</svg>

<style>
	.icon {
		flex: none;
		display: block;
	}
</style>
