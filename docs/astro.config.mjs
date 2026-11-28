// @ts-check
import { execSync } from 'node:child_process';
import { defineConfig } from 'astro/config';
import { unified } from '@astrojs/markdown-remark';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';
import logos from '@iconify-json/logos/icons.json' with { type: 'json' };

const base = '/tessera';

// The logos used in architecture diagrams, bundled so the page fetches no icon set at run time.
const usedLogos = ['postgresql', 'mysql', 'mariadb', 'sqlite', 'duckdb', 'go', 'chrome', 'terminal', 'github-icon', 'github-actions', 'docker-icon', 'json'];
const logoIcons = { ...logos, icons: Object.fromEntries(usedLogos.map((n) => [n, logos.icons[n]])) };

// The newest release tag matching pattern, without its prefix, so install commands name a real
// release. The docs workflow fetches tags; a checkout without them falls back to a placeholder.
function latest(pattern, prefix, fallback) {
	try {
		const tag = execSync(`git describe --tags --abbrev=0 --match "${pattern}"`, { stdio: ['ignore', 'pipe', 'ignore'] });
		return tag.toString().trim().slice(prefix.length);
	} catch {
		return fallback;
	}
}
const vars = {
	__TESS_VERSION__: process.env.TESS_VERSION || latest('v[0-9]*', 'v', '0.5.0'),
	__DUCKDB_VERSION__: process.env.DUCKDB_VERSION || latest('duckdb-v[0-9]*', 'duckdb-v', '0.5.0')
};

// Fills in release versions in text and code, and puts the site base in front of
// root-relative links, which Markdown would otherwise leave pointing outside the site.
function remarkSite() {
	const fill = (s) => s.replace(/__(TESS|DUCKDB)_VERSION__/g, (m) => vars[m]);
	const walk = (node) => {
		if (typeof node.value === 'string') node.value = fill(node.value);
		if (node.type === 'link' && node.url.startsWith('/') && !node.url.startsWith('//') && !node.url.startsWith(base + '/')) {
			node.url = base + node.url;
		}
		node.children?.forEach(walk);
	};
	return walk;
}

export default defineConfig({
	site: 'https://arunksoman.github.io',
	base,
	trailingSlash: 'always',
	markdown: { processor: unified({ remarkPlugins: [remarkSite] }) },
	integrations: [
		// Before Starlight, so mermaid code blocks become diagrams instead of highlighted code.
		mermaid({ autoTheme: true, iconPacks: [{ name: 'logos', icons: logoIcons }] }),
		starlight({
			title: 'Tessera',
			description: 'An open-source database client for PostgreSQL, MySQL, MariaDB, SQLite, and plugins, from the command line, the terminal, or the browser.',
			logo: { src: './src/assets/logo.svg' },
			favicon: '/favicon.svg',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/arunksoman/tessera' }],
			editLink: { baseUrl: 'https://github.com/arunksoman/tessera/edit/main/docs/' },
			customCss: ['./src/styles/custom.css'],
			lastUpdated: true,
			sidebar: [
				{ label: 'Start here', items: [{ autogenerate: { directory: 'start' } }] },
				{ label: 'Guides', items: [{ autogenerate: { directory: 'guides' } }] },
				{ label: 'Databases', items: [{ autogenerate: { directory: 'databases' } }] },
				{ label: 'Building plugins', items: [{ autogenerate: { directory: 'plugins' } }] },
				{ label: 'Reference', items: [{ autogenerate: { directory: 'reference' } }] },
				{ label: 'Project', items: [{ autogenerate: { directory: 'project' } }] }
			]
		})
	]
});
