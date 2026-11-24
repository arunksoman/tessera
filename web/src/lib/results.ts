// Files from a query result already in the page, and transaction text for manual commit.
// Pure functions, tested by scripts/results.test.mjs.
import type { Cell, Field } from './api.ts';

const numeric = new Set(['int', 'decimal', 'float']);
const jsonNumber = /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/;

// Bytes are base64, as in the server's CSV export.
const text = (c: Cell) => (c === null ? '' : typeof c === 'string' ? c : c.b64);

function csvField(s: string) {
	return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

/**
 * CSV with a header row; NULL is an empty field. With neutralize, text that a spreadsheet
 * would run as a formula gets a leading quote, the same rule as tess's CSV writer.
 */
export function resultCSV(fields: Field[], rows: Cell[][], neutralize: boolean): string {
	const safe = (s: string) => (neutralize && /^[=+\-@\t\r]/.test(s) ? "'" + s : s);
	const line = (cells: string[]) => cells.map(csvField).join(',') + '\r\n';
	let out = line(fields.map((f) => safe(f.name)));
	for (const row of rows) {
		out += line(row.map((c, i) => (typeof c === 'string' && !numeric.has(fields[i]?.kind) ? safe(c) : text(c))));
	}
	return out;
}

/** One JSON object per row. Integers and decimals stay exact JSON numbers. */
function objectJSON(fields: Field[], row: Cell[]): string {
	return (
		'{' +
		fields
			.map((f, i) => {
				const c = row[i];
				let v: string;
				if (c === null || c === undefined) v = 'null';
				else if (typeof c === 'string' && numeric.has(f.kind) && jsonNumber.test(c)) v = c;
				else v = JSON.stringify(text(c));
				return JSON.stringify(f.name) + ':' + v;
			})
			.join(',') +
		'}'
	);
}

export function resultJSON(fields: Field[], rows: Cell[][]): string {
	return rows.length ? '[\n' + rows.map((r) => objectJSON(fields, r)).join(',\n') + '\n]\n' : '[]\n';
}

export function resultJSONL(fields: Field[], rows: Cell[][]): string {
	return rows.map((r) => objectJSON(fields, r) + '\n').join('');
}

/** The statement that opens a transaction, for a tile's dialect. */
export function beginStatement(dialect: string): string {
	return dialect === 'mysql' ? 'START TRANSACTION' : 'BEGIN';
}
