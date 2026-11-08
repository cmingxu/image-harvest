import { useEffect, useState } from 'react'

import Pagination from '../components/Pagination'
import { fetchJSON } from '../lib/api'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '../components/ui/table'

interface Domain {
  id: number
  domain: string
  source: string
  score: number
  img_density: number
  hi_res_ratio: number
  samples: number
  last_checked: string
  note: string
}

export default function DiscoverPage() {
  const [domains, setDomains] = useState<Domain[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [limit, setLimit] = useState(100)
  const [error, setError] = useState('')

  useEffect(() => {
    fetchJSON<{ domains?: Domain[]; total?: number }>(`/api/domains?page=${page}&limit=${limit}`)
      .then((d) => {
        setDomains(d.domains ?? [])
        setTotal(d.total ?? 0)
      })
      .catch((e) => setError(String(e)))
  }, [page, limit])

  if (error) {
    return <p className="text-sm text-red-500">{error}</p>
  }

  return (
    <div className="space-y-4">
      <div className="flex items-baseline justify-between">
        <h1 className="text-xl font-semibold">Candidate domains</h1>
        <span className="text-sm text-muted-foreground">{total.toLocaleString()} domains</span>
      </div>
      <p className="text-sm text-muted-foreground">
        Imported straight from the top-1M list — no connectivity probing, no page evaluation. Refresh with{' '}
        <code className="rounded bg-muted px-1">./scripts/import-domains.sh</code> (also runs daily).
      </p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-24">#</TableHead>
            <TableHead>Domain</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Imported</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {domains.map((d, i) => (
            <TableRow key={d.id}>
              <TableCell className="text-muted-foreground">{(page - 1) * limit + i + 1}</TableCell>
              <TableCell className="font-medium">{d.domain}</TableCell>
              <TableCell className="text-muted-foreground">{d.source || '—'}</TableCell>
              <TableCell className="text-muted-foreground">{d.last_checked}</TableCell>
            </TableRow>
          ))}
          {domains.length === 0 && (
            <TableRow>
              <TableCell colSpan={4} className="py-8 text-center text-muted-foreground">
                No domains imported yet — run{' '}
                <code className="rounded bg-muted px-1">./scripts/import-domains.sh</code>.
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
      <Pagination
        page={page}
        limit={limit}
        total={total}
        onPage={setPage}
        onLimit={(l) => {
          setLimit(l)
          setPage(1)
        }}
        label="domains"
        pageSizes={[50, 100, 200, 500]}
      />
    </div>
  )
}
