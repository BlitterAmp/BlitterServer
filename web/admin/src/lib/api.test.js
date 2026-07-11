import { describe, it, expect, vi, beforeEach } from 'vitest'
import { api, ApiError } from './api.js'

function mockFetch(status, body, contentType = 'application/json') {
  return vi.fn(async () => new Response(
    body === undefined ? null : JSON.stringify(body),
    { status, headers: body === undefined ? {} : { 'Content-Type': contentType } },
  ))
}

describe('api client', () => {
  beforeEach(() => { vi.unstubAllGlobals() })

  it('GETs JSON with same-origin credentials', async () => {
    const fetcher = mockFetch(200, { setupComplete: true })
    vi.stubGlobal('fetch', fetcher)
    const out = await api.get('/admin/api/state')
    expect(out.setupComplete).toBe(true)
    const [url, opts] = fetcher.mock.calls[0]
    expect(url).toBe('/admin/api/state')
    expect(opts.credentials).toBe('same-origin')
  })

  it('POSTs JSON bodies and returns null for 204', async () => {
    const fetcher = mockFetch(204, undefined)
    vi.stubGlobal('fetch', fetcher)
    const out = await api.post('/admin/api/setup', { password: 'x'.repeat(12) })
    expect(out).toBeNull()
    const [, opts] = fetcher.mock.calls[0]
    expect(opts.method).toBe('POST')
    expect(JSON.parse(opts.body).password).toHaveLength(12)
    expect(opts.headers['Content-Type']).toBe('application/json')
  })

  it('throws ApiError carrying the problem code on failures', async () => {
    vi.stubGlobal('fetch', mockFetch(401,
      { title: 'Unauthorized', status: 401, code: 'wrong_password' },
      'application/problem+json'))
    const err = await api.post('/admin/api/session', { password: 'nope' }).catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(401)
    expect(err.code).toBe('wrong_password')
  })

  it('supports PUT, PATCH and DELETE', async () => {
    const fetcher = mockFetch(204, undefined)
    vi.stubGlobal('fetch', fetcher)
    await api.put('/admin/api/settings/server', { canonicalUrl: 'https://x' })
    await api.patch('/admin/api/profiles/prf_1', { name: 'N' })
    await api.del('/admin/api/devices/dev_1')
    expect(fetcher.mock.calls.map(c => c[1].method)).toEqual(['PUT', 'PATCH', 'DELETE'])
  })
})
