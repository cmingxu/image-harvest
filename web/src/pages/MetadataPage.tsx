import { useEffect, useState } from 'react'

import { Card, CardContent } from '../components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table'

interface Stats {
  runs: number
  images: number
  saved_images: number
  skipped_images: number
  total_bytes: number
  domains: number
  domains_scored: number
  categories: number
  last_crawl: string
  last_discovered: string
}

interface Meta {
  db_path: string
  db_size_bytes: number
  media_root: string
  config: Record<string, string>
  stats: Stats
}

function bytes(n: number) {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <Card>
      <CardContent className="p-3">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className="mt-1 truncate text-sm font-semibold" title={hint ?? value}>
          {value}
        </div>
      </CardContent>
    </Card>
  )
}

const CONFIG_LABELS: Record<string, string> = {
  depth: 'Crawl depth',
  min_res: 'Minimum resolution',
  quality: 'JPEG quality',
  max_pages: 'Max pages per crawl',
  workers: 'Concurrent fetches',
  discover_limit: 'Domains probed per discovery',
}

export default function MetadataPage() {
  const [meta, setMeta] = useState<Meta | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    fetch('/api/meta')
      .then((r) => r.json())
      .then((d: Meta) => setMeta(d))
      .catch((e) => setError(String(e)))
  }, [])

  if (error) return <p className="text-sm text-red-500">{error}</p>
  if (!meta) return <p className="text-sm text-muted-foreground">Loading…</p>

  const s = meta.stats

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold">Metadata</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Dataset summary and the crawl configuration the workers run with.
        </p>
      </div>

      <section className="space-y-3">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">Storage</h2>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
          <Stat label="DuckDB (local disk)" value={bytes(meta.db_size_bytes)} hint={meta.db_path} />
          <Stat label="Media root" value={meta.media_root} hint={meta.media_root} />
          <Stat label="Images saved" value={`${s.saved_images} · ${bytes(s.total_bytes)}`} />
          <Stat label="Images skipped" value={String(s.skipped_images)} />
        </div>
        <p className="text-xs text-muted-foreground">
          <code className="rounded bg-muted px-1">{meta.db_path}</code> — metadata only, kept on the local disk.
          Images live on the NAS at <code className="rounded bg-muted px-1">{meta.media_root}</code>.
        </p>
      </section>

      <section className="space-y-3">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">Dataset</h2>
        <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
          <Stat label="Crawl runs" value={String(s.runs)} />
          <Stat label="Image records" value={String(s.images)} />
          <Stat label="Domain candidates" value={String(s.domains)} />
          <Stat label="Candidates scored" value={String(s.domains_scored)} />
          <Stat label="Categories" value={String(s.categories)} />
          <Stat label="Last crawl" value={s.last_crawl || '—'} />
        </div>
        <p className="text-xs text-muted-foreground">
          Domain candidates come from the top-1M list: every entry is probed and only network-reachable
          sites are kept. Last discovery: <strong>{s.last_discovered || 'never'}</strong>
        </p>
      </section>

      <section className="space-y-3">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">Crawl config</h2>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Setting</TableHead>
              <TableHead>Value</TableHead>
              <TableHead className="w-[45%]">Env var</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {Object.entries(meta.config).map(([key, value]) => (
              <TableRow key={key}>
                <TableCell>{CONFIG_LABELS[key] ?? key}</TableCell>
                <TableCell className="font-medium">{value}</TableCell>
                <TableCell>
                  <code className="rounded bg-muted px-1 text-xs">{key.toUpperCase()}</code>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <p className="text-xs text-muted-foreground">
          Edit these in <code className="rounded bg-muted px-1">config.env</code> on the deploy host and
          restart the service, or override per run with{' '}
          <code className="rounded bg-muted px-1">./scripts/crawl.sh -depth 1 -min 1600x1200 example.com</code>.
        </p>
      </section>
    </div>
  )
}
