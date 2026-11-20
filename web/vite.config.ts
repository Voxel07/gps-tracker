import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';

export default defineConfig({
  plugins: [
    svelte(),
    {
      name: 'tracker-service-worker',
      apply: 'build',
      enforce: 'post',
      generateBundle(_options, bundle) {
        const source = readFileSync(new URL('./src/sw.js', import.meta.url), 'utf8');
        const assets = [
          '/', '/manifest.webmanifest', '/icons/icon-192.png', '/icons/icon-512.png', '/icons/maskable-512.png',
          ...Object.keys(bundle).filter((file) => file.startsWith('assets/')).sort().map((file) => '/' + file),
        ];
        const revision = createHash('sha256').update(source).update(JSON.stringify(assets)).digest('hex').slice(0, 16);
        this.emitFile({
          type: 'asset', fileName: 'sw.js',
          source: source.replace('__TRACKER_CACHE__', JSON.stringify('tracker-shell-' + revision))
            .replace('__TRACKER_ASSETS__', JSON.stringify(assets)),
        });
      },
    },
  ],
  build: { outDir: '../public/dist', emptyOutDir: true },
});
