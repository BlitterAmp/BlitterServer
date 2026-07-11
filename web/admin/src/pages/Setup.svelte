<script>
  import { api, ApiError } from '../lib/api.js'

  let { onDone } = $props()
  let password = $state('')
  let confirm = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(e) {
    e.preventDefault()
    error = ''
    if (password.length < 10) { error = 'Password must be at least 10 characters.'; return }
    if (password !== confirm) { error = 'Passwords do not match.'; return }
    busy = true
    try {
      await api.post('/admin/api/setup', { password })
      await api.post('/admin/api/session', { password })
      onDone()
    } catch (err) {
      error = err instanceof ApiError && err.code === 'setup_already_complete'
        ? 'Setup is already complete — log in instead.'
        : (err.message || 'Setup failed.')
    } finally {
      busy = false
    }
  }
</script>

<div class="flex min-h-screen items-center justify-center bg-base-200 p-6">
  <div class="card w-full max-w-md bg-base-100 shadow-xl">
    <form class="card-body" onsubmit={submit}>
      <h1 class="card-title text-2xl">Welcome to BlitterServer</h1>
      <p class="text-sm opacity-70">First run: choose the admin password. You'll use it to sign in to this console.</p>
      <label class="form-control">
        <span class="label label-text">Admin password (min 10 characters)</span>
        <input class="input input-bordered w-full" type="password" bind:value={password} autocomplete="new-password" />
      </label>
      <label class="form-control">
        <span class="label label-text">Confirm password</span>
        <input class="input input-bordered w-full" type="password" bind:value={confirm} autocomplete="new-password" />
      </label>
      {#if error}<div class="alert alert-error text-sm">{error}</div>{/if}
      <button class="btn btn-primary mt-2" disabled={busy} type="submit">
        {#if busy}<span class="loading loading-spinner loading-sm"></span>{/if}
        Create admin account
      </button>
    </form>
  </div>
</div>
