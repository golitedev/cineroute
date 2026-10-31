import { writeFileSync } from 'node:fs';
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
export default defineConfig({
	plugins: [
		svelte(),
		tailwindcss(),
		{
			name: 'go-embed-placeholder',
			closeBundle() {
				writeFileSync(
					new URL('../internal/adminui/dist/placeholder.txt', import.meta.url),
					'Run make admin to build the Svelte administration interface before building CineRoute.\n',
				);
			},
		},
	],
	base: '/admin/',
	build: { outDir: '../internal/adminui/dist', emptyOutDir: true },
	server: { proxy: { '/api': 'http://127.0.0.1:8787', '/favicon': 'http://127.0.0.1:8787' } },
});
