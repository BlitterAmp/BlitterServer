<script>
  import { api } from '../lib/api.js'

  let { onAuthLost } = $props()
  let profiles = $state([])
  let error = $state('')
  let name = $state('')
  let pin = $state('')

  async function load() {
    try { profiles = await api.get('/admin/api/profiles') ?? [] }
    catch (err) { if (err.status === 401) { onAuthLost(); return } error = err.message }
  }

  async function create(e) {
    e.preventDefault()
    error = ''
    try {
      await api.post('/admin/api/profiles', { name, pin: pin || undefined })
      name = ''; pin = ''
      await load()
    } catch (err) {
      error = err.code === 'pin_must_be_4_to_8_digits' ? 'PINs are 4–8 digits.' : err.message
    }
  }

  async function rename(p) {
    const next = window.prompt('New name for ' + p.name, p.name)
    if (!next || next === p.name) return
    try { await api.patch('/admin/api/profiles/' + p.profileId, { name: next }); await load() }
    catch (err) { error = err.message }
  }

  async function setPin(p) {
    const next = window.prompt('New 4–8 digit PIN for ' + p.name + ' (leave empty to clear)')
    if (next === null) return
    try {
      await api.patch('/admin/api/profiles/' + p.profileId, { pin: next === '' ? null : next })
      await load()
    } catch (err) {
      error = err.code === 'pin_must_be_4_to_8_digits' ? 'PINs are 4–8 digits.' : err.message
    }
  }

  async function remove(p) {
    if (!window.confirm(`Delete ${p.name}? Their playlists, loves, and history go with them.`)) return
    try { await api.del('/admin/api/profiles/' + p.profileId); await load() }
    catch (err) { error = err.message }
  }

  load()
</script>

{#if error}<div class="alert alert-error mb-4 text-sm">{error}</div>{/if}

<div class="card mb-6 bg-base-100 shadow-sm">
  <form class="card-body" onsubmit={create}>
    <h2 class="card-title text-base">Add a household profile</h2>
    <div class="flex flex-wrap items-end gap-3">
      <label class="form-control">
        <span class="label label-text">Name</span>
        <input class="input input-bordered" bind:value={name} />
      </label>
      <label class="form-control">
        <span class="label label-text">PIN (optional, 4–8 digits)</span>
        <input class="input input-bordered" bind:value={pin} inputmode="numeric" />
      </label>
      <button class="btn btn-primary" disabled={!name} type="submit">Add</button>
    </div>
  </form>
</div>

<div class="card bg-base-100 shadow-sm">
  <div class="card-body p-0">
    <table class="table">
      <thead><tr><th>Name</th><th>PIN</th><th class="text-right">Actions</th></tr></thead>
      <tbody>
        {#each profiles as p (p.profileId)}
          <tr>
            <td class="font-medium">{p.name}</td>
            <td>{p.hasPin ? 'set' : '—'}</td>
            <td class="space-x-1 text-right">
              <button class="btn btn-ghost btn-xs" onclick={() => rename(p)}>Rename</button>
              <button class="btn btn-ghost btn-xs" onclick={() => setPin(p)}>PIN</button>
              <button class="btn btn-ghost btn-xs text-error" onclick={() => remove(p)}>Delete</button>
            </td>
          </tr>
        {:else}
          <tr><td colspan="3" class="text-center text-sm opacity-60">No profiles yet — the apps need at least one.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>
