import { writable } from 'svelte/store'

// ===== Types (documented for clarity) =====
// Agent: a worker definition (e.g. "Code Reviewer"). Has default params.
// AppInstance: a running application = Agent + Harness + Params + Connectors, launched into the service layer.
// Target: a unit of work an agent operates on (issue / PR / message / file).
// Connector: IO adapter (Slack, GitHub, Discord, ...) — input and/or output.
// Harness: the engine that runs the agent (Claude, Codex, Gemini, Local).
// Trace: step-by-step log of what an agent did on a target.

export const harnesses = [
  { id: 'claude',   name: 'Claude Code',   vendor: 'Anthropic',  icon: '✶', color: '#d97757' },
  { id: 'codex',    name: 'Codex',         vendor: 'OpenAI',     icon: '⬡', color: '#10a37f' },
  { id: 'gemini',   name: 'Gemini CLI',    vendor: 'Google',     icon: '◆', color: '#4285f4' },
  { id: 'cursor',   name: 'Cursor Agent',  vendor: 'Cursor',     icon: '◢', color: '#a855f7' },
  { id: 'local',    name: 'Local Runtime', vendor: 'Self-hosted',icon: '◇', color: '#6b6b78' },
]

export const connectors = [
  { id: 'github',   name: 'GitHub',    icon: '◈', color: '#ffffff', kinds: ['input','output'],
    desc: 'Issues, pull requests, commits, and code review comments.' },
  { id: 'slack',    name: 'Slack',     icon: '✦', color: '#e01e5a', kinds: ['input','output'],
    desc: 'Channel messages, DMs, threads, and reactions.' },
  { id: 'discord',  name: 'Discord',   icon: '✲', color: '#5865f2', kinds: ['input','output'],
    desc: 'Server channels and direct messages.' },
  { id: 'email',    name: 'Email',     icon: '✉', color: '#fbbf24', kinds: ['input','output'],
    desc: 'IMAP/SMTP inbox with thread tracking.' },
  { id: 'linear',   name: 'Linear',    icon: '▸', color: '#5e6ad2', kinds: ['input','output'],
    desc: 'Issues, cycles, and project status.' },
  { id: 'gdrive',   name: 'Google Drive', icon: '▽', color: '#1fa463', kinds: ['input'],
    desc: 'Watch folders for new and changed documents.' },
  { id: 'webhook',  name: 'Webhook',   icon: '⇄', color: '#6b7280', kinds: ['input'],
    desc: 'Generic HTTP endpoint for custom integrations.' },
  { id: 'jira',     name: 'Jira',      icon: '◼', color: '#2684ff', kinds: ['input','output'],
    desc: 'Tickets, sprints, and comments.' },
]

export const agents = [
  {
    id: 'code-review',
    name: 'Code Reviewer',
    tagline: 'Reviews pull requests for bugs, style, and security.',
    icon: '🛡',
    color: '#7c6cff',
    category: 'Engineering',
    modes: ['loop', 'periodic', 'oneshot'],
    defaultMode: 'loop',
    defaultHarness: 'claude',
    connectors: ['github'],
    params: [
      { key: 'depth',        label: 'Review depth',    type: 'select',   options: ['Quick','Standard','Deep'], default: 'Standard' },
      { key: 'parallel',     label: 'Parallel reviews',type: 'number',   default: 3,     min:1, max:12 },
      { key: 'autoComment',  label: 'Auto-comment',    type: 'toggle',   default: true },
      { key: 'blockOnSevere',label: 'Block on severe', type: 'toggle',   default: true },
      { key: 'focus',        label: 'Focus areas',     type: 'tags',     default: ['security','perf'] },
    ],
    about: 'Continuously monitors open pull requests and posts review comments. Runs as a forever-loop over your repository.'
  },
  {
    id: 'triage',
    name: 'Issue Triage',
    tagline: 'Labels, prioritizes, and routes incoming issues.',
    icon: '🗂',
    color: '#f472b6',
    category: 'Engineering',
    modes: ['loop'],
    defaultMode: 'loop',
    defaultHarness: 'claude',
    connectors: ['github','linear','jira'],
    params: [
      { key: 'labels',    label: 'Allowed labels',  type: 'tags',   default: ['bug','feature','question'] },
      { key: 'confidence',label: 'Min confidence',  type: 'slider', default: 70, min:0, max:100 },
      { key: 'assign',    label: 'Auto-assign',     type: 'toggle', default: false },
    ],
    about: 'Watches incoming issues, suggests labels and priority, and optionally assigns owners.'
  },
  {
    id: 'support',
    name: 'Support Concierge',
    tagline: 'Answers customer questions and escalates when unsure.',
    icon: '🎧',
    color: '#22d3ee',
    category: 'Support',
    modes: ['loop'],
    defaultMode: 'loop',
    defaultHarness: 'claude',
    connectors: ['slack','discord','email'],
    params: [
      { key: 'tone',       label: 'Tone',            type: 'select', options: ['Friendly','Formal','Concise'], default: 'Friendly' },
      { key: 'escalate',   label: 'Escalate threshold', type: 'slider', default: 50, min:0, max:100 },
      { key: 'draftOnly',  label: 'Draft only',      type: 'toggle', default: false },
    ],
    about: 'Responds to customer messages across channels. Drafts replies for approval or sends directly based on confidence.'
  },
  {
    id: 'refactor',
    name: 'Refactor Sweep',
    tagline: 'One-off large-scale code modernization.',
    icon: '🔧',
    color: '#fbbf24',
    category: 'Engineering',
    modes: ['oneshot'],
    defaultMode: 'oneshot',
    defaultHarness: 'codex',
    connectors: ['github'],
    params: [
      { key: 'pattern',  label: 'Target pattern',  type: 'text',  default: 'callback → async/await' },
      { key: 'scope',    label: 'Scope (paths)',   type: 'text',  default: 'src/**' },
      { key: 'openPRs',  label: 'Open PRs',        type: 'toggle', default: true },
      { key: 'parallel', label: 'Parallel shards', type: 'number', default: 4, min:1, max:16 },
    ],
    about: 'Performs a bounded, one-shot refactor across a codebase and opens draft pull requests per shard.'
  },
  {
    id: 'digest',
    name: 'Daily Digest',
    tagline: 'Summarizes activity and posts a morning brief.',
    icon: '📰',
    color: '#4ade80',
    category: 'Productivity',
    modes: ['periodic'],
    defaultMode: 'periodic',
    defaultHarness: 'gemini',
    connectors: ['slack','github','linear','gdrive'],
    params: [
      { key: 'schedule', label: 'Schedule',  type: 'cron',   default: '0 9 * * *' },
      { key: 'channels', label: 'Post to',   type: 'tags',   default: ['#standup'] },
      { key: 'lookback', label: 'Lookback',  type: 'select', options: ['24h','3d','1w'], default: '24h' },
    ],
    about: 'Compiles activity from your connected sources into a concise daily summary, posted on a schedule.'
  },
  {
    id: 'docs',
    name: 'Docs Sync',
    tagline: 'Keeps documentation in sync with code changes.',
    icon: '📚',
    color: '#60a5fa',
    category: 'Engineering',
    modes: ['periodic'],
    defaultMode: 'periodic',
    defaultHarness: 'claude',
    connectors: ['github','gdrive'],
    params: [
      { key: 'schedule', label: 'Schedule',  type: 'cron', default: '0 */6 * * *' },
      { key: 'docsPath', label: 'Docs path', type: 'text', default: 'docs/' },
      { key: 'autoCommit', label: 'Auto-commit', type: 'toggle', default: false },
    ],
    about: 'Detects code changes that affect documentation and proposes updates via pull requests.'
  },
]

// Target stages for the Kanban
export const stages = [
  { id: 'queued',     name: 'Queued',     tone: 'muted' },
  { id: 'running',    name: 'In progress',tone: 'live' },
  { id: 'review',     name: 'Awaiting review', tone: 'warn' },
  { id: 'done',       name: 'Done',       tone: 'good' },
]

function rid(n=6){ return Math.random().toString(36).slice(2,2+n) }

// Pre-populated running applications
export const apps = writable([
  {
    id: 'app_cr1',
    agentId: 'code-review',
    name: 'Code Review · mitos/core',
    harness: 'claude',
    mode: 'loop',
    status: 'running',
    connectors: [{ id:'github', config:{ repo:'mitos/core', events:['pr.opened','pr.updated'] } }],
    params: { depth:'Deep', parallel:4, autoComment:true, blockOnSevere:true, focus:['security','perf','deps'] },
    createdAt: Date.now() - 1000*60*60*30,
    stats: { done: 142, active: 2, queued: 1 },
  },
  {
    id: 'app_tr1',
    agentId: 'triage',
    name: 'Triage · mitos/core',
    harness: 'claude',
    mode: 'loop',
    status: 'running',
    connectors: [{ id:'github', config:{ repo:'mitos/core', events:['issue.opened'] } }],
    params: { labels:['bug','feature','question','flaky'], confidence:72, assign:false },
    createdAt: Date.now() - 1000*60*60*50,
    stats: { done: 88, active: 0, queued: 3 },
  },
  {
    id: 'app_sup1',
    agentId: 'support',
    name: 'Support · #help',
    harness: 'claude',
    mode: 'loop',
    status: 'running',
    connectors: [
      { id:'slack', config:{ channel:'#help', trigger:'@mitos' } },
      { id:'email', config:{ folder:'Support' } },
    ],
    params: { tone:'Friendly', escalate:50, draftOnly:true },
    createdAt: Date.now() - 1000*60*60*10,
    stats: { done: 27, active: 1, queued: 0 },
  },
  {
    id: 'app_dg1',
    agentId: 'digest',
    name: 'Daily Digest',
    harness: 'gemini',
    mode: 'periodic',
    status: 'idle',
    nextRun: Date.now() + 1000*60*60*8,
    connectors: [{ id:'slack', config:{ channel:'#standup' } }],
    params: { schedule:'0 9 * * *', channels:['#standup'], lookback:'24h' },
    createdAt: Date.now() - 1000*60*60*100,
    stats: { done: 14, active: 0, queued: 0 },
  },
  {
    id: 'app_rf1',
    agentId: 'refactor',
    name: 'Callback → async/await',
    harness: 'codex',
    mode: 'oneshot',
    status: 'running',
    connectors: [{ id:'github', config:{ repo:'mitos/web', branches:['refactor/async'] } }],
    params: { pattern:'callback → async/await', scope:'src/**', openPRs:true, parallel:6 },
    createdAt: Date.now() - 1000*60*60*2,
    stats: { done: 8, active: 4, queued: 2 },
  },
])

// Kanban targets — each is a unit of work an agent is operating on
export const targets = writable([
  // Code review targets
  { id: rid(), appId:'app_cr1', title:'PR #284 — Refactor auth middleware',     stage:'running', kind:'PR',     source:'github', refs:'#284', age:'3m',  attention:false },
  { id: rid(), appId:'app_cr1', title:'PR #281 — Add webhook retries',          stage:'review',  kind:'PR',     source:'github', refs:'#281', age:'18m', attention:true,
    note:'Flagged possible PII in logs' },
  { id: rid(), appId:'app_cr1', title:'PR #279 — Bump deps',                    stage:'done',    kind:'PR',     source:'github', refs:'#279', age:'1h',  attention:false },
  { id: rid(), appId:'app_cr1', title:'PR #285 — Migrate to vitest',            stage:'queued',  kind:'PR',     source:'github', refs:'#285', age:'just now', attention:false },

  // Triage targets
  { id: rid(), appId:'app_tr1', title:'Issue — Login fails on Safari 17',       stage:'running', kind:'Issue',  source:'github', refs:'#1204',age:'2m',  attention:false },
  { id: rid(), appId:'app_tr1', title:'Issue — Feature: export to CSV',         stage:'queued',  kind:'Issue',  source:'github', refs:'#1203',age:'12m', attention:false },
  { id: rid(), appId:'app_tr1', title:'Issue — Webhook delivery is flaky',      stage:'review',  kind:'Issue',  source:'github', refs:'#1201',age:'40m', attention:true,
    note:'Low-confidence label' },
  { id: rid(), appId:'app_tr1', title:'Issue — Dark mode reset bug',            stage:'done',    kind:'Issue',  source:'github', refs:'#1198',age:'2h',  attention:false },

  // Support targets
  { id: rid(), appId:'app_sup1', title:'@dana — billing question (draft ready)',stage:'review',  kind:'Thread', source:'slack',  refs:'#help', age:'5m',  attention:true,
    note:'Draft reply awaiting approval' },
  { id: rid(), appId:'app_sup1', title:'@akira — how to invite teammates',      stage:'running', kind:'Thread', source:'slack',  refs:'#help', age:'1m',  attention:false },
  { id: rid(), appId:'app_sup1', title:'RE: invoice #2290',                     stage:'done',    kind:'Email',  source:'email',  refs:'inbox', age:'30m', attention:false },

  // Refactor (oneshot) — these flow through the board
  { id: rid(), appId:'app_rf1', title:'Shard 3 — services/auth/*',              stage:'running', kind:'Shard',  source:'github', refs:'refactor/async', age:'8m',  attention:false },
  { id: rid(), appId:'app_rf1', title:'Shard 1 — utils/db/*',                   stage:'done',    kind:'Shard',  source:'github', refs:'refactor/async', age:'40m', attention:false },
  { id: rid(), appId:'app_rf1', title:'Shard 2 — api/handlers/*',               stage:'review',  kind:'Shard',  source:'github', refs:'refactor/async', age:'20m', attention:true,
    note:'Generated 14 files — review scope' },
  { id: rid(), appId:'app_rf1', title:'Shard 4 — workers/*',                    stage:'queued',  kind:'Shard',  source:'github', refs:'refactor/async', age:'just now', attention:false },
  { id: rid(), appId:'app_rf1', title:'Shard 5 — test/helpers/*',               stage:'queued',  kind:'Shard',  source:'github', refs:'refactor/async', age:'just now', attention:false },
])

// Sample traces for the inspector
const stepTemplates = {
  plan:  ['Reading PR context','Identifying changed files','Planning review approach'],
  fetch: ['Fetching diff via GitHub API','Loading related test files','Reading contributing guide'],
  think: ['Analyzing security implications','Checking for common pitfalls','Cross-referencing patterns'],
  act:   ['Posting inline comment','Suggesting change','Requesting clarification'],
}

function genTrace(seed) {
  const phases = ['plan','fetch','think','act']
  const out = []
  let t = Date.now() - 1000*60*4
  let n = 0
  for (const p of phases) {
    const opts = stepTemplates[p]
    const count = 1 + (seed % 2)
    for (let i=0;i<count;i++){
      const label = opts[(seed+i) % opts.length]
      const dur = 200 + ((seed*7+i*131)%1400)
      t += dur + 200
      out.push({
        id: n++,
        phase: p,
        label,
        status: 'done',
        dur,
        ts: t,
      })
    }
  }
  // a live step at the end
  out.push({ id:n++, phase:'think', label:'Re-reading surrounding context', status:'live', dur:0, ts: t+800 })
  return out
}

export const traces = writable({})
// seed a couple
;(()=>{
  const map = {}
  const tgts = get(targets)
  tgts.forEach((t,i)=>{ if (t.stage==='running' || t.stage==='review') map[t.id] = genTrace(i+3) })
  traces.set(map)
})()

// live updates — nudge durations and stats
setInterval(() => {
  traces.update(m => {
    for (const id in m) {
      const arr = m[id]
      const last = arr[arr.length-1]
      if (last && last.status === 'live') {
        last.dur += 600 + Math.floor(Math.random()*900)
      }
    }
    return { ...m }
  })
}, 1500)

// helpers
import { get } from 'svelte/store'
export function getAgent(id){ return agents.find(a=>a.id===id) }
export function getHarness(id){ return harnesses.find(h=>h.id===id) }
export function getConnector(id){ return connectors.find(c=>c.id===id) }
