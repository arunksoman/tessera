<script lang="ts">
	import { untrack } from 'svelte';
	import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete';
	import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
	import { MySQL, PostgreSQL, sql } from '@codemirror/lang-sql';
	import { bracketMatching, HighlightStyle, indentOnInput, syntaxHighlighting } from '@codemirror/language';
	import { highlightSelectionMatches, searchKeymap } from '@codemirror/search';
	import { EditorState, Prec } from '@codemirror/state';
	import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers, placeholder } from '@codemirror/view';
	import { tags } from '@lezer/highlight';

	let {
		value = $bindable(''),
		dialect,
		onrun,
		oncancel
	}: { value?: string; dialect: string; onrun: (text: string) => void; oncancel: () => void } = $props();

	let host: HTMLDivElement;
	let view: EditorView | undefined;

	// Colours come from CSS variables, so both themes work without a second editor theme.
	const highlight = HighlightStyle.define([
		{ tag: tags.keyword, color: 'var(--lapis)', fontWeight: '600' },
		{ tag: [tags.string, tags.special(tags.string)], color: 'var(--verdigris)' },
		{ tag: [tags.number, tags.bool, tags.null], color: 'var(--ochre)' },
		{ tag: [tags.comment, tags.lineComment, tags.blockComment], color: 'var(--muted)', fontStyle: 'italic' },
		{ tag: [tags.typeName, tags.standard(tags.name)], color: 'var(--porphyry)' },
		{ tag: [tags.operator, tags.punctuation], color: 'var(--slate)' },
		{ tag: tags.special(tags.name), color: 'var(--terracotta)' }
	]);

	const theme = EditorView.theme({
		'&': { height: '100%', backgroundColor: 'var(--surface)', color: 'var(--text)', fontSize: '13px' },
		'.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.55' },
		'.cm-content': { caretColor: 'var(--text)' },
		'.cm-cursor': { borderLeftColor: 'var(--text)' },
		'.cm-gutters': { backgroundColor: 'var(--surface-2)', color: 'var(--muted)', border: 'none', borderRight: '1px solid var(--border)' },
		'.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--lapis) 6%, transparent)' },
		'.cm-activeLineGutter': { backgroundColor: 'var(--hover)' },
		'&.cm-focused': { outline: 'none' },
		'.cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection': { backgroundColor: 'var(--selected) !important' },
		'.cm-matchingBracket': { backgroundColor: 'var(--selected)', outline: '1px solid var(--lapis)' },
		'.cm-tooltip': { backgroundColor: 'var(--surface)', border: '1px solid var(--border)', color: 'var(--text)' },
		'.cm-tooltip-autocomplete ul li[aria-selected]': { backgroundColor: 'var(--lapis)', color: 'var(--on-accent)' },
		'.cm-panels': { backgroundColor: 'var(--surface-2)', color: 'var(--text)' },
		'.cm-placeholder': { color: 'var(--muted)' }
	});

	/** The selection, or the whole script when nothing is selected. */
	function runText(v: EditorView) {
		const sel = v.state.selection.main;
		return sel.empty ? v.state.doc.toString() : v.state.sliceDoc(sel.from, sel.to);
	}

	$effect(() => {
		const run = () => {
			if (view) onrun(runText(view));
			return true;
		};
		view = new EditorView({
			parent: host,
			state: EditorState.create({
				doc: untrack(() => value),
				extensions: [
					lineNumbers(),
					highlightActiveLineGutter(),
					history(),
					drawSelection(),
					indentOnInput(),
					bracketMatching(),
					closeBrackets(),
					autocompletion(),
					highlightActiveLine(),
					highlightSelectionMatches(),
					syntaxHighlighting(highlight),
					sql({ dialect: dialect === 'mysql' ? MySQL : PostgreSQL, upperCaseKeywords: true }),
					placeholder('Write SQL, then press Ctrl+Enter to run. Select text to run only that part.'),
					Prec.highest(
						keymap.of([
							{ key: 'Mod-Enter', run },
							{ key: 'F5', run },
							{ key: 'Escape', run: () => (oncancel(), false) }
						])
					),
					keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...completionKeymap, indentWithTab]),
					theme,
					// tess serve sends a fresh nonce with the page, so the editor's styles pass the CSP.
					EditorView.cspNonce.of(document.querySelector<HTMLMetaElement>('meta[name="tess-csp-nonce"]')?.content ?? ''),
					EditorView.updateListener.of((u) => {
						if (u.docChanged) value = u.state.doc.toString();
					})
				]
			})
		});
		view.focus();
		return () => view?.destroy();
	});

	export function focus() {
		view?.focus();
	}
</script>

<div class="editor" bind:this={host}></div>

<style>
	.editor {
		height: 100%;
		overflow: hidden;
	}
</style>
