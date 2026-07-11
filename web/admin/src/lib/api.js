// Thin fetch wrapper for the same-origin admin API. Sessions ride the
// blitter_admin cookie; errors surface as ApiError carrying the RFC 9457
// problem code so pages can branch on it.

export class ApiError extends Error {
  constructor(status, code, title) {
    super(title || `HTTP ${status}`)
    this.status = status
    this.code = code || ''
  }
}

async function request(method, path, body) {
  const opts = { method, credentials: 'same-origin', headers: {} }
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const resp = await fetch(path, opts)
  if (resp.status === 204 || resp.status === 202 && resp.headers.get('Content-Type') === null) {
    return null
  }
  const isJSON = (resp.headers.get('Content-Type') || '').includes('json')
  const payload = isJSON ? await resp.json().catch(() => null) : null
  if (!resp.ok) {
    throw new ApiError(resp.status, payload?.code, payload?.title)
  }
  return payload
}

export const api = {
  get: (path) => request('GET', path),
  post: (path, body) => request('POST', path, body),
  put: (path, body) => request('PUT', path, body),
  patch: (path, body) => request('PATCH', path, body),
  del: (path) => request('DELETE', path),
}
