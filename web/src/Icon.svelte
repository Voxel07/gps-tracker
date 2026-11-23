<script lang="ts">
  const paths = {
    sun: 'M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0Z',
    moon: 'M20.5 14A8.7 8.7 0 0 1 10 3.5 8.8 8.8 0 1 0 20.5 14Z',
    layers: 'm3 8 9-5 9 5-9 5-9-5Zm0 4.5 9 5 9-5m-18 4.5 9 5 9-5',
    route: 'M5 17V9a4 4 0 0 1 8 0v6a3 3 0 0 0 6 0V7M7 19a2 2 0 1 1-4 0 2 2 0 0 1 4 0Zm14-14a2 2 0 1 1-4 0 2 2 0 0 1 4 0Z',
    history: 'M3 11a9 9 0 1 1 2 7M3 5v6h6m3-5v6l4 2',
    photo: 'M4 4h16v16H4zM4 16l5-5 4 4 3-3 4 4M16 8h.01',
    left: 'm14.5 6-6 6 6 6',
    right: 'm9.5 6 6 6-6 6',
    down: 'm6 9 6 6 6-6',
    up: 'm6 15 6-6 6 6',
    close: 'm6 6 12 12M6 18 18 6',
    refresh: 'M20 8a8.5 8.5 0 1 0 .4 7M20 3v5h-5',
    edit: 'm4 16 12-12 4 4L8 20l-5 1 1-5Zm10-10 4 4',
    external: 'M6 18 18 6M6 6h12v12',
    upload: 'M12 16V3m-5 5 5-5 5 5M4 16v5h16v-5',
    download: 'M12 3v13m-5-5 5 5 5-5M4 16v5h16v-5',
    device: 'M7 2h10v20H7zM11 18h2',
    pin: 'M19 10c0 5-7 11-7 11S5 15 5 10a7 7 0 1 1 14 0Zm-5 0a2 2 0 1 1-4 0 2 2 0 0 1 4 0Z',
    check: 'm5 12 4 4L19 6',
    expand: 'M4 9V4h5M15 4h5v5M20 15v5h-5M9 20H4v-5',
    shrink: 'M9 4v5H4M20 9h-5V4M15 20v-5h5M4 15h5v5',
  };
  let { name, size = 17 }: { name: keyof typeof paths; size?: number } = $props();
</script>

<svg class="icon" aria-hidden="true" focusable="false" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
  <path d={paths[name]} />
</svg>
