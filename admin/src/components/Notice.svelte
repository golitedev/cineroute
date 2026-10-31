<script lang="ts">
	let { error = '', success = '' }: { error?: string; success?: string } = $props();
</script>

{#if error}<div class="notice error" role="alert">{error}</div>{/if}
{#if success}<div class="notice success" role="status">{success}</div>{/if}
