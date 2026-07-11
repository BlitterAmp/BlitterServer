<script>
  import { api } from '../lib/api.js'

  let { onDone } = $props()
  let password = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(e) {
    e.preventDefault()
    error = ''
    busy = true
    try {
      await api.post('/admin/api/session', { password })
      onDone()
    } catch (err) {
      error = err.status === 401 ? 'Wrong password.' : (err.message || 'Login failed.')
    } finally {
      busy = false
    }
  }
</script>

<div class="flex min-h-screen items-center justify-center bg-base-200 p-6">
  <div class="card w-full max-w-md bg-base-100 shadow-xl">
    <form class="card-body" onsubmit={submit}>
      <h1 class="card-title text-2xl">BlitterServer Admin</h1>
      <label class="form-control">
        <span class="label label-text">Admin password</span>
        <input class="input input-bordered w-full" type="password" bind:value={password} autocomplete="current-password" />
      </label>
      {#if error}<div class="alert alert-error text-sm">{error}</div>{/if}
      <button class="btn btn-primary mt-2" disabled={busy} type="submit">
        {#if busy}<span class="loading loading-spinner loading-sm"></span>{/if}
        Sign in
      </button>
    </form>
  </div>
</div>
