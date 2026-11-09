import { sveltekit } from '@sveltejs/kit/vite';
import adapter from '@sveltejs/adapter-static';
import { defineConfig } from 'vite';

declare const process: { env: Record<string, string | undefined> };

// A single-page app, embedded in tess from build/app. tess serves index.html for every path
// that is not a file, and allows its inline bootstrap script by hash in the CSP.
export default defineConfig({
	plugins: [
		sveltekit({
			adapter: adapter({ pages: 'build/app', assets: 'build/app', fallback: 'index.html' })
		})
	],
	server: {
		// `npm run dev` talks to a running tess serve; set TESS_URL to its address.
		proxy: { '/api': process.env.TESS_URL ?? 'http://127.0.0.1:8484' }
	}
});
