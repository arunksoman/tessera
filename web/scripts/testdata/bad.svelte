<script lang="ts">
  let { cell }: { cell: string } = $props();
</script>

{#if cell}
  <td>{@html cell}</td>
{/if}
