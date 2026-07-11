<script>
  import { api } from '../lib/api.js'

  let { onAuthLost } = $props()
  let canonicalUrl = $state('')
  let transcode = $state(null)
  let error = $state('')
  let saved = $state('')

  async function load() {
    try {
      const server = await api.get('/admin/api/settings/server')
      canonicalUrl = server?.canonicalUrl ?? ''
      transcode = await api.get('/admin/api/settings/transcode')
    } catch (err) {
      if (err.status === 401) { onAuthLost(); return }
      error = err.message
    }
  }

  async function saveServer(e) {
    e.preventDefault()
    error = ''; saved = ''
    try {
      await api.put('/admin/api/settings/server', { canonicalUrl: canonicalUrl || null })
      saved = 'server'
    } catch (err) {
      error = err.code === 'canonical_url_must_be_absolute'
        ? 'The canonical URL must be absolute (e.g. https://music.example.net).'
        : err.message
    }
  }

  async function saveTranscode(e) {
    e.preventDefault()
    error = ''; saved = ''
    try {
      await api.put('/admin/api/settings/transcode', {
        defaultFormat: transcode.defaultFormat,
        defaultBitrateKbps: Number(transcode.defaultBitrateKbps),
        artifactCacheMaxBytes: Number(transcode.artifactCacheMaxBytes),
      })
      saved = 'transcode'
    } catch (err) {
      error = err.message
    }
  }

  load()
</script>

{#if error}<div class="alert alert-error mb-4 text-sm">{error}</div>{/if}

<div class="grid gap-6 md:grid-cols-2">
  <div class="card bg-base-100 shadow-sm">
    <form class="card-body" onsubmit={saveServer}>
      <h2 class="card-title text-base">Server</h2>
      <label class="form-control">
        <span class="label label-text">Canonical URL</span>
        <input class="input input-bordered w-full font-mono" placeholder="https://music.example.net" bind:value={canonicalUrl} />
        <span class="label label-text-alt">The public address embedded in pairing QRs — never a LAN IP unless devices genuinely reach it there.</span>
      </label>
      <div class="card-actions items-center">
        <button class="btn btn-primary btn-sm" type="submit">Save</button>
        {#if saved === 'server'}<span class="text-sm text-success">Saved.</span>{/if}
      </div>
    </form>
  </div>

  {#if transcode}
    <div class="card bg-base-100 shadow-sm">
      <form class="card-body" onsubmit={saveTranscode}>
        <h2 class="card-title text-base">Downloads & transcoding</h2>
        {#if transcode.ffmpegAvailable === false}
          <div class="alert alert-warning text-sm">ffmpeg is not on this server's PATH — only original-format downloads are possible.</div>
        {/if}
        <div class="flex flex-wrap gap-3">
          <label class="form-control">
            <span class="label label-text">Default format</span>
            <select class="select select-bordered" bind:value={transcode.defaultFormat}>
              <option value="original">original</option>
              <option value="aac">aac</option>
            </select>
          </label>
          <label class="form-control">
            <span class="label label-text">AAC bitrate (kbps)</span>
            <select class="select select-bordered" bind:value={transcode.defaultBitrateKbps}>
              {#each [128, 192, 256, 320] as b}<option value={b}>{b}</option>{/each}
            </select>
          </label>
          <label class="form-control">
            <span class="label label-text">Artifact cache budget (bytes)</span>
            <input class="input input-bordered font-mono" bind:value={transcode.artifactCacheMaxBytes} inputmode="numeric" />
          </label>
        </div>
        <div class="card-actions items-center">
          <button class="btn btn-primary btn-sm" type="submit">Save</button>
          {#if saved === 'transcode'}<span class="text-sm text-success">Saved.</span>{/if}
        </div>
      </form>
    </div>
  {/if}
</div>
