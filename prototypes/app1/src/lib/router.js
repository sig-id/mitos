import { writable } from 'svelte/store'

// Minimal hash router: #/path/segments
function parse() {
  const h = location.hash.replace(/^#\/?/, '')
  return h.split('/').filter(Boolean)
}

const store = writable(parse())

export const route = { subscribe: store.subscribe }

window.addEventListener('hashchange', () => store.set(parse()))

export function go(path) {
  const clean = path.replace(/^#?\/?/, '')
  location.hash = '/' + clean
}

export function here(...segs) {
  const r = parse()
  return segs.every((s, i) => r[i] === s)
}
