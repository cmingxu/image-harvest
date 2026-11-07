import { useEffect, useState } from 'react'

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
  const [error, setError] = useState('')

  useEffect(() => {
    fetch('/api/domains')
      .then((r) => r.json())
      .then((d) => setDomains(d.domains ?? []))
      .catch((e) => setError(String(e)))
  }, [])

  if (error) {
    return <p className="text-sm text-red-500">{error}</p>
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Candidate domains</h1>
      <p className="text-sm text-muted-foreground">
        Probing measures homepage image density and samples a few images for resolution. Run{' '}
        <code className="rounded bg-muted px-1">image-harvest -discover</code> to refresh.
      </p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>#</TableHead>
            <TableHead>Domain</TableHead>
            <TableHead>Score</TableHead>
            <TableHead className="text-right">Density</TableHead>
            <TableHead className="text-right">Hi-res</TableHead>
            <TableHead>Last checked</TableHead>
            <TableHead>Note</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {domains.map((d, i) => (
            <TableRow key={d.id}>
              <TableCell>{i + 1}</TableCell>
              <TableCell className="font-medium">{d.domain}</TableCell>
              <TableCell>{d.score.toFixed(1)}</TableCell>
              <TableCell className="text-right">{d.img_density}</TableCell>
              <TableCell className="text-right">{(d.hi_res_ratio * 100).toFixed(0)}%</TableCell>
              <TableCell>{d.last_checked}</TableCell>
              <TableCell className="max-w-[240px] truncate text-muted-foreground" title={d.note}>
                {d.note}
              </TableCell>
            </TableRow>
          ))}
          {domains.length === 0 && (
            <TableRow>
              <TableCell colSpan={7} className="py-8 text-center text-muted-foreground">
                No domains probed yet — run <code className="rounded bg-muted px-1">image-harvest -discover</code>.
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}
