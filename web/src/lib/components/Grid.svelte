<script lang="ts">
	import { cellText, type Cell, type Field } from '#lib/api.ts';
	import Dialog from './Dialog.svelte';

	// A row-virtualized grid: only visible rows are in the DOM. Values render as text only.
	let {
		fields,
		rows,
		version = 0,
		sort = null,
		onsort,
		editable,
		pending,
		pendingVersion = 0,
		onedit,
		onnearend,
		label = 'Results'
	}: {
		fields: Field[];
		rows: Cell[][];
		version?: number;
		sort?: { field: string; desc: boolean } | null;
		onsort?: (field: string) => void;
		editable?: (r: number, c: number) => boolean;
		pending?: Map<string, Cell>;
		pendingVersion?: number;
		onedit?: (r: number, c: number, v: Cell) => void;
		onnearend?: () => void;
		label?: string;
	} = $props();

	const ROW = 28;
	const OVERSCAN = 6;
	const NUM_W = 56;

	let viewport: HTMLDivElement;
	let scrollTop = $state(0);
	let height = $state(400);
	let widths = $state<number[]>([]);
	let active = $state<{ r: number; c: number } | null>(null);
	let editing = $state<{ r: number; c: number; text: string; isNull: boolean } | null>(null);
	let viewing = $state<{ title: string; text: string } | null>(null);

	$effect(() => {
		// New result shape: size columns from their names, reset the cursor.
		widths = fields.map((f) => Math.min(320, Math.max(90, f.name.length * 8 + 40)));
		active = null;
		editing = null;
	});

	const count = $derived((void version, rows.length));
	const first = $derived(Math.max(0, Math.floor(scrollTop / ROW) - OVERSCAN));
	const last = $derived(Math.min(count, Math.ceil((scrollTop + height) / ROW) + OVERSCAN));
	const visible = $derived.by(() => {
		void version;
		void pendingVersion;
		const out: number[] = [];
		for (let r = first; r < last; r++) out.push(r);
		return out;
	});
	const template = $derived(`${NUM_W}px ${widths.map((w) => w + 'px').join(' ')}`);
	const totalWidth = $derived(NUM_W + widths.reduce((a, b) => a + b, 0));

	$effect(() => {
		if (onnearend && count > 0 && last >= count - OVERSCAN) onnearend();
	});

	function valueAt(r: number, c: number): Cell {
		// Rows live in a plain array, so read the counters to redraw when it changes.
		void version;
		void pendingVersion;
		const p = pending?.get(`${r}:${c}`);
		return p !== undefined ? p : rows[r][c];
	}
	const isPending = (r: number, c: number) => (void pendingVersion, !!pending?.has(`${r}:${c}`));

	function startResize(e: PointerEvent, c: number) {
		e.preventDefault();
		e.stopPropagation();
		const startX = e.clientX;
		const start = widths[c];
		const target = e.currentTarget as HTMLElement;
		target.setPointerCapture(e.pointerId);
		const move = (ev: PointerEvent) => (widths[c] = Math.max(48, start + ev.clientX - startX));
		const up = () => {
			target.removeEventListener('pointermove', move);
			target.removeEventListener('pointerup', up);
		};
		target.addEventListener('pointermove', move);
		target.addEventListener('pointerup', up);
	}

	function scrollIntoView(r: number) {
		const top = r * ROW;
		if (top < viewport.scrollTop) viewport.scrollTop = top;
		else if (top + ROW * 2 > viewport.scrollTop + height) viewport.scrollTop = top + ROW * 2 - height;
	}

	function select(r: number, c: number) {
		active = { r: Math.max(0, Math.min(count - 1, r)), c: Math.max(0, Math.min(fields.length - 1, c)) };
		scrollIntoView(active.r);
	}

	function open(r: number, c: number) {
		if (editable?.(r, c)) {
			const v = valueAt(r, c);
			editing = { r, c, text: typeof v === 'string' ? v : '', isNull: v === null };
		} else {
			const v = valueAt(r, c);
			viewing = { title: fields[c].name, text: typeof v === 'string' ? v : cellText(v, 1_000_000) };
		}
	}

	function commit(v: Cell) {
		if (!editing) return;
		onedit?.(editing.r, editing.c, v);
		editing = null;
		viewport.focus();
	}

	function keydown(e: KeyboardEvent) {
		if (editing || !count) return;
		const a = active ?? { r: 0, c: 0 };
		const page = Math.max(1, Math.floor(height / ROW) - 1);
		const moves: Record<string, () => void> = {
			ArrowDown: () => select(a.r + 1, a.c),
			ArrowUp: () => select(a.r - 1, a.c),
			ArrowRight: () => select(a.r, a.c + 1),
			ArrowLeft: () => select(a.r, a.c - 1),
			PageDown: () => select(a.r + page, a.c),
			PageUp: () => select(a.r - page, a.c),
			Home: () => select(e.ctrlKey ? 0 : a.r, 0),
			End: () => select(e.ctrlKey ? count - 1 : a.r, fields.length - 1),
			Enter: () => active && open(a.r, a.c),
			F2: () => active && open(a.r, a.c)
		};
		if (moves[e.key]) {
			e.preventDefault();
			moves[e.key]();
		} else if ((e.ctrlKey || e.metaKey) && e.key === 'c' && active) {
			const v = valueAt(a.r, a.c);
			navigator.clipboard?.writeText(v === null ? '' : typeof v === 'string' ? v : cellText(v, 1_000_000));
		}
	}

	function editKey(e: KeyboardEvent) {
		if (!editing) return;
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			commit(editing.isNull ? null : editing.text);
		} else if (e.key === 'Escape') {
			e.preventDefault();
			e.stopPropagation();
			editing = null;
			viewport.focus();
		}
	}

	function focusInput(el: HTMLTextAreaElement) {
		el.focus();
		el.select();
	}
</script>

<div
	class="grid"
	role="grid"
	aria-label={label}
	aria-rowcount={count + 1}
	aria-colcount={fields.length}
	tabindex="0"
	bind:this={viewport}
	bind:clientHeight={height}
	onscroll={() => (scrollTop = viewport.scrollTop)}
	onkeydown={keydown}
	style:--cols={template}
>
	<div class="header" role="row" style:width="{totalWidth}px">
		<div class="num" role="columnheader">#</div>
		{#each fields as f, c (c)}
			<div class="th" role="columnheader" aria-sort={sort && sort.field === f.name ? (sort.desc ? 'descending' : 'ascending') : undefined}>
				<button class="th-label" title="{f.name}{f.type ? ` (${f.type})` : ''}" disabled={!onsort} onclick={() => onsort?.(f.name)}>
					<span class="name">{f.name}</span>
					{#if sort && sort.field === f.name}<span class="arrow">{sort.desc ? '▼' : '▲'}</span>{/if}
				</button>
				<span class="resize" role="separator" aria-orientation="vertical" onpointerdown={(e) => startResize(e, c)}></span>
			</div>
		{/each}
	</div>
	<div class="body" style:height="{count * ROW}px" style:width="{totalWidth}px">
		{#each visible as r (r)}
			<div class="tr" role="row" aria-rowindex={r + 2} style:transform="translateY({r * ROW}px)">
				<div class="num" role="rowheader">{r + 1}</div>
				{#each fields as f, c (c)}
					{@const v = valueAt(r, c)}
					<!-- Keys are handled on the grid itself, following the ARIA grid pattern. -->
					<!-- svelte-ignore a11y_click_events_have_key_events -->
					<div
						class="td"
						role="gridcell"
						tabindex="-1"
						class:null={v === null}
						class:pending={isPending(r, c)}
						class:active={active?.r === r && active?.c === c}
						class:numeric={f.kind === 'int' || f.kind === 'decimal' || f.kind === 'float'}
						onclick={() => (active = { r, c })}
						ondblclick={() => open(r, c)}
					>
						{#if editing?.r === r && editing?.c === c}
							<div class="editor">
								<textarea
									class="mono"
									rows="1"
									bind:value={editing.text}
									placeholder={editing.isNull ? 'NULL' : ''}
									oninput={() => editing && (editing.isNull = false)}
									onkeydown={editKey}
									onblur={() => editing && commit(editing.isNull ? null : editing.text)}
									use:focusInput
								></textarea>
								<button class="null-btn" title="Set NULL" onmousedown={(e) => { e.preventDefault(); commit(null); }}>NULL</button>
							</div>
						{:else}
							{cellText(v)}
						{/if}
					</div>
				{/each}
			</div>
		{/each}
	</div>
</div>

{#if viewing}
	<Dialog title={viewing.title} wide onclose={() => (viewing = null)}>
		<pre class="value mono">{viewing.text}</pre>
	</Dialog>
{/if}

<style>
	.grid {
		position: relative;
		overflow: auto;
		height: 100%;
		background: var(--surface);
		font-family: var(--font-mono);
		font-size: 12px;
		contain: strict;
	}
	.grid:focus-visible {
		outline-offset: -2px;
	}
	.header {
		position: sticky;
		top: 0;
		z-index: 2;
		display: grid;
		grid-template-columns: var(--cols);
		height: var(--row);
		background: var(--surface-2);
		border-bottom: 1px solid var(--border);
		font-family: var(--font-ui);
		font-weight: 600;
	}
	.th {
		position: relative;
		display: flex;
		border-right: 1px solid var(--border);
		min-width: 0;
	}
	.th-label {
		display: flex;
		align-items: center;
		gap: 4px;
		width: 100%;
		min-width: 0;
		border: 0;
		background: none;
		padding: 0 8px;
		cursor: pointer;
		font-weight: 600;
		text-align: left;
	}
	.th-label:disabled {
		cursor: default;
		opacity: 1;
	}
	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.arrow {
		color: var(--lapis);
		font-size: 9px;
	}
	.resize {
		position: absolute;
		right: -3px;
		top: 0;
		bottom: 0;
		width: 6px;
		cursor: col-resize;
		z-index: 1;
	}
	.body {
		position: relative;
	}
	.tr {
		position: absolute;
		left: 0;
		top: 0;
		display: grid;
		grid-template-columns: var(--cols);
		height: var(--row);
		width: 100%;
	}
	.tr:hover .td,
	.tr:hover .num {
		background: var(--hover);
	}
	.num {
		position: sticky;
		left: 0;
		z-index: 1;
		padding: 0 8px;
		line-height: var(--row);
		text-align: right;
		color: var(--muted);
		background: var(--surface-2);
		border-right: 1px solid var(--border);
		border-bottom: 1px solid var(--border);
		font-size: 11px;
	}
	.header .num {
		z-index: 3;
		line-height: var(--row);
	}
	.td {
		position: relative;
		padding: 0 8px;
		line-height: var(--row);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		border-right: 1px solid var(--border);
		border-bottom: 1px solid var(--border);
		background: var(--surface);
		cursor: default;
	}
	.td.numeric {
		text-align: right;
		font-variant-numeric: tabular-nums;
	}
	.td.null {
		color: var(--muted);
		font-style: italic;
	}
	.td.pending {
		background: var(--pending);
	}
	.td.active {
		outline: 2px solid var(--lapis);
		outline-offset: -2px;
	}
	.editor {
		position: absolute;
		inset: 0;
		display: flex;
		z-index: 4;
	}
	.editor textarea {
		flex: 1;
		min-width: 0;
		resize: none;
		border: 2px solid var(--lapis);
		background: var(--surface);
		padding: 4px 6px;
		font-size: 12px;
		line-height: 16px;
		outline: none;
	}
	.null-btn {
		border: 1px solid var(--border);
		background: var(--surface-2);
		font-size: 10px;
		padding: 0 4px;
		cursor: pointer;
	}
	.value {
		margin: 0;
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 60vh;
		overflow: auto;
	}
</style>
