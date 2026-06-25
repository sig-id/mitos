<script>
  export let id
  export let size = 16
  export let color = 'var(--text-dim)'
  export let label = ''
  $: c = (
    ({ github:'#e6edf3', slack:'#e01e5a', discord:'#5865f2', email:'#fbbf24',
       linear:'#5e6ad2', gdrive:'#1fa463', webhook:'#6b7280', jira:'#2684ff' })[id]
    || '#9aa'
  )
  $: glyph = (
    ({ github:'◈', slack:'✦', discord:'✲', email:'✉',
       linear:'▸', gdrive:'▽', webhook:'⇄', jira:'◼' })[id]
    || '•'
  )
</script>

<span class="connector" style="--c:{color || c}; --s:{size}px" title={label || id} aria-label={label || id}>
  <span class="g">{glyph}</span>
</span>

<style>
  .connector {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--s);
    height: var(--s);
    border-radius: 5px;
    background: color-mix(in srgb, var(--c) 16%, transparent);
    color: var(--c);
    font-size: calc(var(--s) * 0.6);
    line-height: 1;
    flex: none;
  }
  .g { transform: translateY(-0.5px); }
</style>
