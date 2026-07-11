<script>
  import { api } from '../lib/api.js'

  let { onAuthLost } = $props()
  let state_ = $state(null)
  let source = $state(null)
  let error = $state('')

  async function load() {
    try {
      state_ = await api.get('/admin/api/state')
      source = await api.get('/admin/api/source/filesystem')
    } catch (err) {
      if (err.status === 401) { onAuthLost(); return }
      error = err.message
    }
  }
  load()
</script>

{#if error}
  <div class="alert alert-error">{error}</div>
{:else if !state_}
  <span class="loading loading-dots"></span>
{:else}
  <div class="stats stats-vertical w-full bg-base-100 shadow-sm sm:stats-horizontal">
    <div class="stat">
      <div class="stat-title">Profiles</div>
      <div class="stat-value">{state_.profileCount}</div>
      <div class="stat-desc"><a class="link" href="#/profiles">manage</a></div>
    </div>
    <div class="stat">
      <div class="stat-title">Devices</div>
      <div class="stat-value">{state_.deviceCount}</div>
      <div class="stat-desc"><a class="link" href="#/devices">manage</a></div>
    </div>
    <div class="stat">
      <div class="stat-title">Pending pairings</div>
      <div class="stat-value">{state_.pendingPairings}</div>
      <div class="stat-desc"><a class="link" href="#/pairings">review</a></div>
    </div>
  </div>

  <div class="mt-6 grid gap-4 md:grid-cols-2">
    <div class="card bg-base-100 shadow-sm">
      <div class="card-body">
        <h2 class="card-title">Music source</h2>
        {#if source?.configured}
          <p class="break-all font-mono text-sm">{source.path}</p>
          <p class="text-sm opacity-70">
            {#if source.scanning}Scanning…{:else if source.lastScanAt}Last scan {new Date(source.lastScanAt).toLocaleString()}{:else}Not scanned yet{/if}
            {#if source.lastScanError}<span class="text-error"> — {source.lastScanError}</span>{/if}
          </p>
        {:else}
          <p class="text-sm opacity-70">No source configured — point BlitterServer at a music directory.</p>
        {/if}
        <div class="card-actions"><a class="btn btn-sm" href="#/source">Configure</a></div>
      </div>
    </div>
    <div class="card bg-base-100 shadow-sm">
      <div class="card-body">
        <h2 class="card-title">Checklist</h2>
        <ul class="space-y-1 text-sm">
          <li>{state_.source?.linked ? '✅' : '⬜'} Music source linked</li>
          <li>{state_.canonicalUrlSet ? '✅' : '⬜'} Canonical URL set (needed for QR pairing)</li>
          <li>{state_.profileCount > 0 ? '✅' : '⬜'} At least one profile created</li>
          <li>{state_.integrations?.lidarr === 'configured' ? '✅' : '⬜'} Lidarr configured <span class="opacity-60">(optional)</span></li>
          <li>{state_.integrations?.lastfm === 'configured' ? '✅' : '⬜'} last.fm configured <span class="opacity-60">(optional)</span></li>
        </ul>
      </div>
    </div>
  </div>
{/if}
