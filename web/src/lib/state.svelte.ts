import { api, randomId, type Profile, type Ref, type Tile } from './api';

export type Tab =
	| { id: string; kind: 'sql'; profile: string; title: string; sql: string }
	| { id: string; kind: 'browse'; profile: string; title: string; ref: Ref };

export type Theme = 'system' | 'light' | 'dark';

class AppState {
	tiles = $state<Tile[]>([]);
	profiles = $state<Profile[]>([]);
	tabs = $state<Tab[]>([]);
	active = $state<string | null>(null);
	signedOut = $state(false);
	loadError = $state('');
	/** Bumped after DDL, so open trees reload. */
	treeVersion = $state<Record<string, number>>({});
	theme = $state<Theme>(readTheme());
	toast = $state<{ text: string; kind: 'ok' | 'error' } | null>(null);
	private toastTimer: ReturnType<typeof setTimeout> | undefined;

	activeTab = $derived(this.tabs.find((t) => t.id === this.active) ?? null);

	async load() {
		try {
			const [tiles, profiles] = await Promise.all([api.tiles(), api.profiles()]);
			this.tiles = tiles;
			this.profiles = profiles;
			this.loadError = '';
		} catch (e) {
			this.loadError = (e as Error).message;
		}
	}

	async reloadProfiles() {
		this.profiles = await api.profiles();
	}

	tile(id: string) {
		return this.tiles.find((t) => t.id === id);
	}

	profile(name: string) {
		return this.profiles.find((p) => p.name === name);
	}

	openSQL(profile: string, sql = '') {
		const n = this.tabs.filter((t) => t.kind === 'sql' && t.profile === profile).length + 1;
		const tab: Tab = { id: randomId(), kind: 'sql', profile, title: `${profile} · SQL ${n}`, sql };
		this.tabs.push(tab);
		this.active = tab.id;
	}

	openBrowse(profile: string, ref: Ref) {
		const key = JSON.stringify(ref);
		const open = this.tabs.find((t) => t.kind === 'browse' && t.profile === profile && JSON.stringify(t.ref) === key);
		if (open) {
			this.active = open.id;
			return;
		}
		const tab: Tab = { id: randomId(), kind: 'browse', profile, title: ref[ref.length - 1]?.name ?? profile, ref };
		this.tabs.push(tab);
		this.active = tab.id;
	}

	closeTab(id: string) {
		const i = this.tabs.findIndex((t) => t.id === id);
		if (i < 0) return;
		const [tab] = this.tabs.splice(i, 1);
		if (tab.kind === 'sql') api.closeTab(tab.profile, tab.id);
		if (this.active === id) this.active = this.tabs[Math.min(i, this.tabs.length - 1)]?.id ?? null;
	}

	closeProfileTabs(profile: string) {
		for (const t of this.tabs.filter((t) => t.profile === profile)) this.closeTab(t.id);
	}

	cycleTab(step: number) {
		if (!this.tabs.length) return;
		const i = this.tabs.findIndex((t) => t.id === this.active);
		this.active = this.tabs[(i + step + this.tabs.length) % this.tabs.length].id;
	}

	refreshTree(profile: string) {
		this.treeVersion[profile] = (this.treeVersion[profile] ?? 0) + 1;
	}

	setTheme(t: Theme) {
		this.theme = t;
		try {
			if (t === 'system') localStorage.removeItem('tessera-theme');
			else localStorage.setItem('tessera-theme', t);
		} catch {
			// storage blocked: the choice lasts for this page only
		}
		if (t === 'system') delete document.documentElement.dataset.theme;
		else document.documentElement.dataset.theme = t;
	}

	notify(text: string, kind: 'ok' | 'error' = 'ok') {
		this.toast = { text, kind };
		clearTimeout(this.toastTimer);
		this.toastTimer = setTimeout(() => (this.toast = null), kind === 'error' ? 6000 : 3000);
	}
}

function readTheme(): Theme {
	try {
		const t = localStorage.getItem('tessera-theme');
		if (t === 'light' || t === 'dark') return t;
	} catch {
		// ignore
	}
	return 'system';
}

export const app = new AppState();
