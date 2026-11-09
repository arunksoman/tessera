// Client for tess serve's /api. Values stay strings, so integers and decimals never pass
// through a JavaScript number; bytes that are not UTF-8 arrive as {b64}.

export type Cell = string | null | { b64: string };
export type Field = { name: string; type?: string; kind: string };
export type Segment = { kind: string; name: string };
export type Ref = Segment[];

export type FormField = {
	name: string;
	label: string;
	type: 'text' | 'int' | 'bool' | 'enum' | 'secret' | 'file' | string;
	required?: boolean;
	options?: string[];
	default?: string;
};
export type Tile = { id: string; name: string; style: { Color: string; Glyph: string }; form: FormField[] };

export type Profile = {
	name: string;
	tile: string;
	env?: string;
	readOnly: boolean;
	noHistory: boolean;
	values: Record<string, string>;
	secrets: Record<string, boolean>;
	warnings?: string[];
};
export type SaveProfile = {
	from?: string;
	tile: string;
	env: string;
	readOnly: boolean;
	noHistory: boolean;
	values: Record<string, string>;
	secrets: Record<string, string>;
};
export type Saved = Profile & { connected: boolean; error?: string };

export type TreeNode = { ref: Ref; kind: string; label: string; hasChildren: boolean; browsable: boolean };
export type Layout = { fields: Field[]; key: string[]; note?: string; estimate: number };
export type Filter = { field: string; op: string; value: Cell };
export type BrowseRequest = {
	ref: Ref;
	sort?: { field: string; desc: boolean }[];
	filter?: Filter[];
	after?: Cell[];
	offset?: number;
	limit?: number;
};
export type BrowseResult = { layout: Layout; fields: Field[]; rows: Cell[][] };

export type Prompt = { statement: string; verb: string; reason: string; risk: string; env?: string };
export type ExecEvent =
	| { t: 'schema'; stmt: number; fields: Field[] }
	| { t: 'rows'; stmt: number; rows: Cell[][] }
	| { t: 'done'; stmt: number; affected?: number; hasAffected?: boolean; tag?: string }
	| { t: 'truncated'; stmt: number; affected?: number }
	| { t: 'error'; stmt: number; error: string }
	| { t: 'end'; inTx?: boolean; ddl?: boolean };

export type Change = { key: Cell[]; old: Record<string, Cell>; set: Record<string, Cell> };
export type Plan = { id: string; steps: { preview: string; expectRows: number }[]; atomic: boolean };

export class ApiError extends Error {
	constructor(
		readonly status: number,
		message: string,
		readonly code = '',
		readonly prompts: Prompt[] = []
	) {
		super(message);
	}
}

/** Called when the session is gone, so the app can say how to sign in again. */
export let onSignedOut = () => {};
export function setSignedOutHandler(f: () => void) {
	onSignedOut = f;
}

async function fail(res: Response): Promise<never> {
	let body: { error?: string; code?: string; prompts?: Prompt[] } = {};
	try {
		body = await res.json();
	} catch {
		// not JSON
	}
	if (res.status === 401) onSignedOut();
	throw new ApiError(res.status, body.error || res.statusText, body.code, body.prompts);
}

async function call<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
	const res = await fetch(path, {
		method,
		headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
		body: body === undefined ? undefined : JSON.stringify(body),
		signal,
		credentials: 'same-origin'
	});
	if (!res.ok) return fail(res);
	if (res.status === 204) return undefined as T;
	return res.json();
}

const conn = (profile: string) => `/api/conns/${encodeURIComponent(profile)}`;

export const api = {
	tiles: () => call<Tile[]>('GET', '/api/tiles'),
	profiles: () => call<Profile[]>('GET', '/api/profiles'),
	saveProfile: (name: string, p: SaveProfile) => call<Saved>('PUT', `/api/profiles/${encodeURIComponent(name)}`, p),
	deleteProfile: (name: string) => call<void>('DELETE', `/api/profiles/${encodeURIComponent(name)}`),
	tree: (profile: string, ref: Ref, cursor = '') =>
		call<{ nodes: TreeNode[]; next: string }>('POST', `${conn(profile)}/tree`, { ref, cursor }),
	browse: (profile: string, req: BrowseRequest, signal?: AbortSignal) =>
		call<BrowseResult>('POST', `${conn(profile)}/browse`, req, signal),
	plan: (profile: string, ref: Ref, changes: Change[]) => call<Plan>('POST', `${conn(profile)}/plan`, { ref, changes }),
	apply: (profile: string, id: string, confirm: boolean) =>
		call<void>('POST', `${conn(profile)}/apply`, { id, confirm }),
	closeTab: (profile: string, tab: string) =>
		call<void>('DELETE', `${conn(profile)}/tabs/${encodeURIComponent(tab)}`).catch(() => {}),

	/** Streams a script's results. Aborting the signal cancels the statement on the server. */
	async exec(
		profile: string,
		tab: string,
		sql: string,
		confirm: string[],
		onEvent: (e: ExecEvent) => void,
		signal: AbortSignal
	): Promise<void> {
		const res = await fetch(`${conn(profile)}/exec`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ tab, sql, confirm }),
			signal,
			credentials: 'same-origin'
		});
		if (!res.ok) return fail(res);
		const reader = res.body!.pipeThrough(new TextDecoderStream()).getReader();
		let buf = '';
		for (;;) {
			const { value, done } = await reader.read();
			if (done) break;
			buf += value;
			let nl: number;
			while ((nl = buf.indexOf('\n')) >= 0) {
				const line = buf.slice(0, nl);
				buf = buf.slice(nl + 1);
				if (line) onEvent(JSON.parse(line));
			}
		}
	}
};

/** Text for display only: never parsed as HTML. */
export function cellText(c: Cell, max = 300): string {
	if (c === null) return 'NULL';
	if (typeof c === 'object') {
		const bin = atob(c.b64);
		let hex = '\\x';
		for (let i = 0; i < Math.min(bin.length, max / 2); i++) hex += bin.charCodeAt(i).toString(16).padStart(2, '0');
		return bin.length > max / 2 ? hex + '…' : hex;
	}
	const s = c.length > max ? c.slice(0, max) + '…' : c;
	return s.replace(/\r?\n/g, '⏎').replace(/\t/g, '→');
}

export function randomId(): string {
	const b = new Uint8Array(9);
	crypto.getRandomValues(b);
	return btoa(String.fromCharCode(...b)).replace(/[+/=]/g, '');
}
