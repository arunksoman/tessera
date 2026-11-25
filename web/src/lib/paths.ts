// Paths on the machine running tess serve, for the file picker. The separator comes from the
// server, so these work the same for Windows and Unix paths. Pure functions, tested by
// scripts/paths.test.mjs.

export type Crumb = { label: string; path: string };

/** True for /x, \\server\x, and C:\x. */
export function isAbsolute(p: string): boolean {
	return /^([/\\]|[A-Za-z]:[/\\])/.test(p);
}

export function join(dir: string, name: string, sep: string): string {
	return dir.endsWith(sep) ? dir + name : dir + sep + name;
}

/** The folders from the root down to path, each with its full path. */
export function crumbs(path: string, sep: string): Crumb[] {
	const root = sep === '\\' ? (/^[A-Za-z]:\\/.test(path) ? path.slice(0, 3) : '\\') : '/';
	const out: Crumb[] = [{ label: root, path: root }];
	let acc = root;
	for (const part of path.slice(root.length).split(sep)) {
		if (!part) continue;
		acc = join(acc, part, sep);
		out.push({ label: part, path: acc });
	}
	return out;
}

export function baseName(path: string, sep: string): string {
	return path.slice(path.lastIndexOf(sep) + 1);
}

const dbNames = /\.(db|db3|sqlite|sqlite3|s3db|sl3)$/i;

/** Whether a file name looks like a SQLite database. */
export function isDatabaseFile(name: string): boolean {
	return dbNames.test(name);
}

export function formatSize(n: number): string {
	if (n < 1024) return `${n} B`;
	const units = ['KB', 'MB', 'GB', 'TB'];
	let v = n / 1024;
	let i = 0;
	for (; v >= 1024 && i < units.length - 1; i++) v /= 1024;
	return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}
