<script>
  import { api } from './lib/api.js'
  import { route, goto } from './lib/router.js'
  import Setup from './pages/Setup.svelte'
  import Login from './pages/Login.svelte'
  import Dashboard from './pages/Dashboard.svelte'
  import Source from './pages/Source.svelte'
  import Profiles from './pages/Profiles.svelte'
  import Pairings from './pages/Pairings.svelte'
  import Devices from './pages/Devices.svelte'
  import Settings from './pages/Settings.svelte'
  import Integrations from './pages/Integrations.svelte'

  let phase = $state('loading') // loading | setup | login | app

  const pages = {
    dashboard: { title: 'Dashboard', component: Dashboard },
    source: { title: 'Music Source', component: Source },
    profiles: { title: 'Profiles', component: Profiles },
    pairings: { title: 'Pairing', component: Pairings },
    devices: { title: 'Devices', component: Devices },
    settings: { title: 'Settings', component: Settings },
    integrations: { title: 'Integrations', component: Integrations },
  }

  async function boot() {
    phase = 'loading'
    try {
      const ping = await api.get('/v1/ping')
      if (!ping?.setupComplete) { phase = 'setup'; return }
      await api.get('/admin/api/state')
      phase = 'app'
    } catch {
      phase = 'login'
    }
  }

  async function logout() {
    try { await api.del('/admin/api/session') } catch {}
    phase = 'login'
  }

  boot()
</script>

{#if phase === 'loading'}
  <div class="flex min-h-screen items-center justify-center">
    <span class="loading loading-ring loading-lg"></span>
  </div>
{:else if phase === 'setup'}
  <Setup onDone={() => { phase = 'app'; goto('dashboard') }} />
{:else if phase === 'login'}
  <Login onDone={() => { phase = 'app'; goto('dashboard') }} />
{:else}
  <div class="min-h-screen bg-base-200">
    <div class="navbar bg-base-100 shadow-sm">
      <div class="flex-1">
        <span class="px-4 text-xl font-semibold">BlitterServer</span>
      </div>
      <div class="flex-none">
        <button class="btn btn-ghost btn-sm" onclick={logout}>Log out</button>
      </div>
    </div>
    <div class="mx-auto flex max-w-6xl gap-6 p-6">
      <ul class="menu w-48 shrink-0 rounded-box bg-base-100 shadow-sm">
        {#each Object.entries(pages) as [name, page]}
          <li>
            <a href={'#/' + name} class={$route === name ? 'menu-active' : ''}>{page.title}</a>
          </li>
        {/each}
      </ul>
      <main class="min-w-0 flex-1">
        {#if pages[$route]}
          {@const Page = pages[$route].component}
          <h1 class="mb-4 text-2xl font-semibold">{pages[$route].title}</h1>
          <Page onAuthLost={() => { phase = 'login' }} />
        {:else}
          <div class="alert alert-warning">Unknown page — <a class="link" href="#/dashboard">back to the dashboard</a>.</div>
        {/if}
      </main>
    </div>
  </div>
{/if}
