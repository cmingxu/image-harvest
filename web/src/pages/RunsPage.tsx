import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '../components/ui/table'

interface Run {
  id: number
  domain: string
  started_at: string
  duration_s: number
  depth: number
  pages_visited: number
  images_downloaded: number
}

export default function RunsPage() {
  const [runs, setRuns] = useState<Run[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    fetch('/api/runs')
      .then((r) => r.json())
      .then((d) => setRuns(d.runs ?? []))
      .catch((e) => setError(String(e)))
  }, [])

  if (error) {
    return <p className="text-sm text-red-500">{error}</p>
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Crawl runs</h1>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-10">#</TableHead>
            <TableHead>Domain</TableHead>
            <TableHead>Started</TableHead>
            <TableHead className="text-right">Depth</TableHead>
            <TableHead className="text-right">Pages</TableHead>
            <TableHead className="text-right">Images</TableHead>
            <TableHead className="text-right">Duration</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {runs.map((r) => (
            <TableRow key={r.id}>
              <TableCell>{r.id}</TableCell>
              <TableCell>
                <Link to={`/runs/${r.id}`} className="font-medium text-primary hover:underline">
                  {r.domain}
                </Link>
              </TableCell>
              <TableCell>{r.started_at}</TableCell>
              <TableCell className="text-right">{r.depth}</TableCell>
              <TableCell className="text-right">{r.pages_visited}</TableCell>
              <TableCell className="text-right">{r.images_downloaded}</TableCell>
              <TableCell className="text-right">{r.duration_s.toFixed(0)}s</TableCell>
            </TableRow>
          ))}
          {runs.length === 0 && (
            <TableRow>
              <TableCell colSpan={7} className="py-8 text-center text-muted-foreground">
                No runs yet — run <code className="rounded bg-muted px-1">image-harvest &lt;domain&gt;</code> first.
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}
