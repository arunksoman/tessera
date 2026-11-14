<script lang="ts">
  // Mentioning {@html} or innerHTML in a comment is fine.
  let { cell }: { cell: string } = $props();
  const note = "the text {@html x} is just a string";
</script>

<td title={note}>{cell}</td>
