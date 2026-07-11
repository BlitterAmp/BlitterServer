// Minimal hash router: the server only ever serves /admin/, so all
// navigation lives after the #.
import { writable } from 'svelte/store'

function current() {
  return window.location.hash.replace(/^#\/?/, '') || 'dashboard'
}

export const route = writable(typeof window === 'undefined' ? 'dashboard' : current())

if (typeof window !== 'undefined') {
  window.addEventListener('hashchange', () => route.set(current()))
}

export function goto(name) {
  window.location.hash = '#/' + name
}
