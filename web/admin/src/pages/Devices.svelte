<script>
  import { api } from '../lib/api.js'

  let { onAuthLost } = $props()
  let devices = $state([])
  let error = $state('')

  async function load() {
    try { devices = await api.get('/admin/api/devices') ?? [] }
    catch (err) { if (err.status === 401) { onAuthLost(); return } error = err.message }
  }

  async function revoke(d) {
    if (!window.confirm(`Revoke ${d.name}? Its tokens stop working immediately.`)) return
    try { await api.del('/admin/api/devices/' + d.deviceId); await load() }
    catch (err) { error = err.message }
  }

  load()
</script>

{#if error}<div class="alert alert-error mb-4 text-sm">{error}</div>{/if}

<div class="card bg-base-100 shadow-sm">
  <div class="card-body p-0">
    <table class="table">
      <thead><tr><th>Device</th><th>Type</th><th>Paired</th><th>Last seen</th><th class="text-right">Actions</th></tr></thead>
      <tbody>
        {#each devices as d (d.deviceId)}
          <tr>
            <td class="font-medium">{d.name}</td>
            <td><span class="badge badge-ghost badge-sm">{d.type}</span></td>
            <td class="text-sm">{new Date(d.pairedAt).toLocaleString()}</td>
            <td class="text-sm">{d.lastSeenAt ? new Date(d.lastSeenAt).toLocaleString() : '—'}</td>
            <td class="text-right">
              <button class="btn btn-ghost btn-xs text-error" onclick={() => revoke(d)}>Revoke</button>
            </td>
          </tr>
        {:else}
          <tr><td colspan="5" class="text-center text-sm opacity-60">No paired devices — send someone to the Pairing page.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>
