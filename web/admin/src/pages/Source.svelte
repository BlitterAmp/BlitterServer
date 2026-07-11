<script>
  import { api } from '../lib/api.js'

  let { onAuthLost } = $props()
  let source = $state(null)
  let path = $state('')
  let error = $state('')
  let busy = $state(false)
  let pollTimer = null

  async function load() {
    try {
      source = await api.get('/admin/api/source/filesystem')
      if (source?.path) path = source.path
      if (source?.scanning && !pollTimer) {
        pollTimer = setInterval(async () => {
          source = await api.get('/admin/api/source/filesystem').catch(() => source)
          if (!source?.scanning) { clearInterval(pollTimer); pollTimer = null }
        }, 1500)
      }
    } catch (err) {
      if (err.status === 401) { onAuthLost(); return }
      error = err.message
    }
  }

  async function save(e) {
    e.preventDefault()
    error = ''
    busy = true
    try {
      await api.put('/admin/api/source/filesystem', { path })
      await load()
    } catch (err) {
      error = err.code === 'path_not_a_directory'
        ? 'That path does not exist on the server (or is not a directory).'
        : err.message
    } finally {
      busy = false
    }
  }

  async function rescan() {
    error = ''
    try { await api.post('/admin/api/source/filesystem/scan'); await load() }
    catch (err) { error = err.message }
  }

  async function unlink() {
    error = ''
    try { await api.del('/admin/api/source/filesystem'); await load() }
    catch (err) { error = err.message }
  }

  $effect(() => () => { if (pollTimer) clearInterval(pollTimer) })
  load()
</script>

<div class="card bg-base-100 shadow-sm">
  <form class="card-body" onsubmit={save}>
    <p class="text-sm opacity-70">
      A directory of music files is a complete source — metadata comes from the files' own tags.
      The path is on the machine running BlitterServer.
    </p>
    <label class="form-control">
      <span class="label label-text">Music directory</span>
      <input class="input input-bordered w-full font-mono" placeholder="/srv/music" bind:value={path} />
    </label>
    {#if error}<div class="alert alert-error text-sm">{error}</div>{/if}
    <div class="card-actions mt-2">
      <button class="btn btn-primary" disabled={busy || !path} type="submit">Save & scan</button>
      {#if source?.configured}
        <button class="btn" type="button" onclick={rescan} disabled={source?.scanning}>Rescan</button>
        <button class="btn btn-outline btn-error" type="button" onclick={unlink}>Unlink</button>
      {/if}
    </div>
    {#if source?.scanning}
      <div class="mt-2 flex items-center gap-2 text-sm"><span class="loading loading-spinner loading-sm"></span> Scanning the library…</div>
    {:else if source?.lastScanAt}
      <p class="mt-2 text-sm opacity-70">Last scan: {new Date(source.lastScanAt).toLocaleString()}
        {#if source.lastScanError}<span class="text-error"> — {source.lastScanError}</span>{/if}
      </p>
    {/if}
  </form>
</div>
