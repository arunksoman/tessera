import type { IconName } from './icons/index.ts';

/** One entry of a context menu, or a separator line. */
export type MenuItem =
	| { label: string; action: () => void; icon?: IconName; hint?: string; disabled?: boolean; danger?: boolean }
	| 'separator';

/** Where a menu opens, in viewport pixels, and what it holds. */
export type MenuAt = { x: number; y: number; items: MenuItem[] };

/** Drops separators at either end and repeated ones, left where optional items were skipped. */
export function tidy(items: MenuItem[]): MenuItem[] {
	const out: MenuItem[] = [];
	for (const it of items) {
		if (it === 'separator' && (out.length === 0 || out[out.length - 1] === 'separator')) continue;
		out.push(it);
	}
	while (out[out.length - 1] === 'separator') out.pop();
	return out;
}

/** Copies text to the clipboard, quietly doing nothing where the browser refuses. */
export function copyText(text: string) {
	navigator.clipboard?.writeText(text).catch(() => {});
}
