/** Fetch JSON, retrying while the server reports a busy database (import/crawl). */
export async function fetchJSON<T>(url: string, attempts = 6, delayMs = 800): Promise<T> {
  let last: unknown
  for (let i = 0; i < attempts; i++) {
    const res = await fetch(url)
    if (res.ok) return (await res.json()) as T
    last = new Error(`HTTP ${res.status}`)
    // 503 = DuckDB write lock held by an import or crawl
    if (res.status !== 503) throw last
    await new Promise((r) => setTimeout(r, delayMs))
  }
  throw last
}
