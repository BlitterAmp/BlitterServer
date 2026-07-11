import { describe, it, expect, vi } from 'vitest'
import { render, fireEvent, screen } from '@testing-library/svelte'
import Login from './Login.svelte'

describe('Login page', () => {
  it('renders and submits the password to the session endpoint', async () => {
    const fetcher = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetcher)
    const onDone = vi.fn()
    render(Login, { props: { onDone } })

    const input = document.querySelector('input[type=password]')
    await fireEvent.input(input, { target: { value: 'hunter2hunter2' } })
    await fireEvent.click(screen.getByRole('button', { name: /sign in/i }))
    await new Promise(r => setTimeout(r, 0))

    expect(fetcher).toHaveBeenCalledOnce()
    const [url, opts] = fetcher.mock.calls[0]
    expect(url).toBe('/admin/api/session')
    expect(JSON.parse(opts.body).password).toBe('hunter2hunter2')
    expect(onDone).toHaveBeenCalled()
  })

  it('shows an error on wrong password', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ title: 'Unauthorized', status: 401, code: 'wrong_password' }),
      { status: 401, headers: { 'Content-Type': 'application/problem+json' } })))
    render(Login, { props: { onDone: vi.fn() } })
    await fireEvent.input(document.querySelector('input[type=password]'), { target: { value: 'nope' } })
    await fireEvent.click(screen.getByRole('button', { name: /sign in/i }))
    await new Promise(r => setTimeout(r, 0))
    expect(screen.getByText('Wrong password.')).toBeTruthy()
  })
})
