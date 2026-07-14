<script>
  import { api } from '../lib/api.js'

  let { onAuthLost } = $props()
  let lidarr = $state(null)
  let lastfm = $state(null)
  let fanart = $state(null)
  let discogs = $state(null)
  let loading = $state(true)
  let error = $state('')
  let lidarrForm = $state({ baseUrl: '', apiKey: '' })
  let lastfmForm = $state({ apiKey: '', sharedSecret: '' })
  let fanartKey = $state('')
  let discogsToken = $state('')
  let testResult = $state(null)

  async function load() {
    try {
      lidarr = await api.get('/admin/api/integrations/lidarr')
      ;[lastfm, fanart, discogs] = await Promise.all([
        api.get('/admin/api/integrations/lastfm'),
        api.get('/admin/api/integrations/fanart'),
        api.get('/admin/api/integrations/discogs'),
      ])
      if (lidarr?.baseUrl) lidarrForm.baseUrl = lidarr.baseUrl
    } catch (err) {
      if (err.status === 401) { onAuthLost(); return }
      error = err.message
    } finally { loading = false }
  }

  async function saveLidarr(e) {
    e.preventDefault(); error = ''; testResult = null
    try {
      await api.put('/admin/api/integrations/lidarr', lidarrForm)
      lidarrForm.apiKey = ''
      await load()
    } catch (err) { error = err.message }
  }

  async function testLidarr() {
    error = ''; testResult = null
    try { testResult = await api.post('/admin/api/integrations/lidarr/test') }
    catch (err) { error = err.message }
  }

  async function deleteLidarr() {
    error = ''; testResult = null
    try { await api.del('/admin/api/integrations/lidarr'); await load() }
    catch (err) { error = err.message }
  }

  async function saveLastfm(e) {
    e.preventDefault(); error = ''
    try {
      await api.put('/admin/api/integrations/lastfm', lastfmForm)
      lastfmForm = { apiKey: '', sharedSecret: '' }
      await load()
    } catch (err) { error = err.message }
  }

  async function deleteLastfm() {
    error = ''
    try { await api.del('/admin/api/integrations/lastfm'); await load() }
    catch (err) { error = err.message }
  }

  async function saveFanart(e) {
    e.preventDefault(); error = ''
    try { await api.put('/admin/api/integrations/fanart', { apiKey: fanartKey }); fanartKey = ''; await load() }
    catch (err) { error = err.message }
  }

  async function deleteFanart() {
    error = ''
    try { await api.del('/admin/api/integrations/fanart'); await load() }
    catch (err) { error = err.message }
  }

  async function saveDiscogs(e) {
    e.preventDefault(); error = ''
    try { await api.put('/admin/api/integrations/discogs', { personalToken: discogsToken }); discogsToken = ''; await load() }
    catch (err) { error = err.message }
  }

  async function deleteDiscogs() {
    error = ''
    try { await api.del('/admin/api/integrations/discogs'); await load() }
    catch (err) { error = err.message }
  }

  load()
</script>

{#if loading}<span class="loading loading-spinner" aria-label="Loading integrations"></span>{/if}
{#if error}<div class="alert alert-error mb-4 text-sm">{error}</div>{/if}

<div class="grid gap-6 md:grid-cols-2">
  <div class="card bg-base-100 shadow-sm">
    <form class="card-body" onsubmit={saveLidarr}>
      <h2 class="card-title text-base">
        Lidarr
        <span class="badge badge-sm {lidarr?.configured ? 'badge-success' : 'badge-ghost'}">{lidarr?.configured ? 'configured' : 'not configured'}</span>
      </h2>
      <p class="text-sm opacity-70">Acquisition config for a future adapter — connectivity can be tested today; loves do not trigger downloads yet.</p>
      <label class="form-control">
        <span class="label label-text">Base URL</span>
        <input class="input input-bordered w-full font-mono" placeholder="http://192.168.1.5:8686" bind:value={lidarrForm.baseUrl} />
      </label>
      <label class="form-control">
        <span class="label label-text">API key {#if lidarr?.apiKeySet}<span class="opacity-60">(set — enter to replace)</span>{/if}</span>
        <input class="input input-bordered w-full font-mono" type="password" bind:value={lidarrForm.apiKey} autocomplete="off" />
      </label>
      {#if testResult}
        <div class="alert {testResult.ok ? 'alert-success' : 'alert-error'} text-sm">
          {testResult.ok ? `Connected${testResult.version ? ' — Lidarr ' + testResult.version : ''}.` : testResult.error}
        </div>
      {/if}
      <div class="card-actions">
        <button class="btn btn-primary btn-sm" type="submit" disabled={!lidarrForm.baseUrl || !lidarrForm.apiKey}>Save</button>
        {#if lidarr?.configured}
          <button class="btn btn-sm" type="button" onclick={testLidarr}>Test connection</button>
          <button class="btn btn-outline btn-error btn-sm" type="button" onclick={deleteLidarr}>Remove</button>
        {/if}
      </div>
    </form>
  </div>

  <div class="card bg-base-100 shadow-sm">
    <form class="card-body" onsubmit={saveLastfm}>
      <h2 class="card-title text-base">
        last.fm
        <span class="badge badge-sm {lastfm?.configured ? 'badge-success' : 'badge-ghost'}">{lastfm?.configured ? 'configured' : 'not configured'}</span>
      </h2>
      <p class="text-sm opacity-70">Instance API credentials. Household members connect their own accounts from the apps.</p>
      <label class="form-control">
        <span class="label label-text">API key</span>
        <input class="input input-bordered w-full font-mono" type="password" bind:value={lastfmForm.apiKey} autocomplete="off" />
      </label>
      <label class="form-control">
        <span class="label label-text">Shared secret</span>
        <input class="input input-bordered w-full font-mono" type="password" bind:value={lastfmForm.sharedSecret} autocomplete="off" />
      </label>
      <div class="card-actions">
        <button class="btn btn-primary btn-sm" type="submit" disabled={!lastfmForm.apiKey || !lastfmForm.sharedSecret}>Save</button>
        {#if lastfm?.configured}
          <button class="btn btn-outline btn-error btn-sm" type="button" onclick={deleteLastfm}>Remove</button>
        {/if}
      </div>
    </form>
  </div>

  <div class="card bg-base-100 shadow-sm">
    <form class="card-body" onsubmit={saveFanart}>
      <h2 class="card-title text-base">fanart.tv
        <span class="badge badge-sm {fanart?.configured ? 'badge-success' : 'badge-ghost'}">{fanart?.configured ? 'configured' : 'not configured'}</span>
      </h2>
      <p class="text-sm opacity-70">Optional artist artwork enrichment. The API key is write-only.</p>
      <label class="form-control">
        <span class="label label-text">API key {#if fanart?.configured}<span class="opacity-60">(set - enter to replace)</span>{/if}</span>
        <input class="input input-bordered w-full font-mono" type="password" bind:value={fanartKey} autocomplete="off" />
      </label>
      <div class="card-actions">
        <button class="btn btn-primary btn-sm" type="submit" disabled={!fanartKey}>Save</button>
        {#if fanart?.configured}<button class="btn btn-outline btn-error btn-sm" type="button" onclick={deleteFanart}>Remove</button>{/if}
      </div>
    </form>
  </div>

  <div class="card bg-base-100 shadow-sm">
    <form class="card-body" onsubmit={saveDiscogs}>
      <h2 class="card-title text-base">Discogs
        <span class="badge badge-sm {discogs?.configured ? 'badge-success' : 'badge-ghost'}">{discogs?.configured ? 'configured' : 'not configured'}</span>
      </h2>
      <p class="text-sm opacity-70">Album and artist artwork fallback. The personal access token is write-only.</p>
      <label class="form-control">
        <span class="label label-text">Personal access token {#if discogs?.configured}<span class="opacity-60">(set - enter to replace)</span>{/if}</span>
        <input class="input input-bordered w-full font-mono" type="password" bind:value={discogsToken} autocomplete="off" />
      </label>
      <div class="card-actions">
        <button class="btn btn-primary btn-sm" type="submit" disabled={!discogsToken}>Save</button>
        {#if discogs?.configured}<button class="btn btn-outline btn-error btn-sm" type="button" onclick={deleteDiscogs}>Remove</button>{/if}
      </div>
    </form>
  </div>
</div>
