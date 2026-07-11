<script>
  import QRCode from 'qrcode'
  import { api } from '../lib/api.js'

  let { onAuthLost } = $props()
  let pending = $state([])
  let error = $state('')
  let qr = $state(null) // { code, expiresAt, dataUrl }
  let pollTimer = null

  async function load() {
    try { pending = await api.get('/admin/api/pairings') ?? [] }
    catch (err) { if (err.status === 401) { onAuthLost(); return } error = err.message }
  }

  async function act(p, verb) {
    error = ''
    try { await api.post(`/admin/api/pairings/${p.pairingId}/${verb}`); await load() }
    catch (err) { error = err.message }
  }

  async function mintQR() {
    error = ''
    try {
      const out = await api.post('/admin/api/pair-codes')
      qr = { code: out.code, expiresAt: out.expiresAt, dataUrl: await QRCode.toDataURL(out.qrPayload, { width: 240 }) }
    } catch (err) {
      error = err.code === 'canonical_url_not_configured'
        ? 'Set the canonical URL in Settings first — the QR must carry a stable server address.'
        : err.message
    }
  }

  $effect(() => {
    pollTimer = setInterval(load, 3000)
    return () => clearInterval(pollTimer)
  })
  load()
</script>

{#if error}<div class="alert alert-error mb-4 text-sm">{error}</div>{/if}

<div class="grid gap-6 md:grid-cols-2">
  <div class="card bg-base-100 shadow-sm">
    <div class="card-body">
      <h2 class="card-title text-base">Link a device (QR)</h2>
      <p class="text-sm opacity-70">Scan with the BlitterAmp app — one scan carries the server address and a single-use code.</p>
      {#if qr}
        <img class="mx-auto rounded-box" src={qr.dataUrl} alt="pairing QR" />
        <p class="text-center font-mono text-lg tracking-widest">{qr.code}</p>
        <p class="text-center text-xs opacity-60">expires {new Date(qr.expiresAt).toLocaleTimeString()}</p>
      {/if}
      <div class="card-actions">
        <button class="btn btn-primary btn-sm" onclick={mintQR}>{qr ? 'New code' : 'Show QR code'}</button>
      </div>
    </div>
  </div>

  <div class="card bg-base-100 shadow-sm">
    <div class="card-body">
      <h2 class="card-title text-base">Pending requests</h2>
      <p class="text-sm opacity-70">Devices that entered a PIN code and are waiting for approval.</p>
      {#each pending as p (p.pairingId)}
        <div class="flex items-center justify-between rounded-box border border-base-300 p-3">
          <div>
            <p class="font-medium">{p.deviceName} <span class="badge badge-ghost badge-sm">{p.deviceType}</span></p>
            <p class="font-mono text-sm tracking-widest">{p.code}</p>
          </div>
          <div class="space-x-1">
            <button class="btn btn-success btn-sm" onclick={() => act(p, 'approve')}>Approve</button>
            <button class="btn btn-ghost btn-sm" onclick={() => act(p, 'deny')}>Deny</button>
          </div>
        </div>
      {:else}
        <p class="text-sm opacity-60">Nothing waiting.</p>
      {/each}
    </div>
  </div>
</div>
