import { mount } from 'svelte';
import 'leaflet/dist/leaflet.css';
import './style.css';
import App from './App.svelte';

mount(App, { target: document.getElementById('app')! });

if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  void navigator.serviceWorker.register('/sw.js').catch((error) => console.error('App shell could not be registered', error));
}
