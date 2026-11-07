import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import Pagination from '../components/Pagination'
import { Card, CardContent } from '../components/ui/card'

interface Run {
  id: number
  domain: string
  started_at: string
  duration_s: number
  depth: number
  pages_visited: number
  images_downloaded: number
}

interface Image {
  id: number
  url: string
  width: number
  height: number
  ext: string
  saved_path: string
  size_bytes: number
  skipped: string
}

interface Similar extends Image {
  distance: number
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <Card>
      <CardContent className="p-3">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className="mt-1 truncate text-sm font-semibold" title={value}>
          {value}
        </div>
      </CardContent>
    </Card>
  )
}

const FILTERS = ['saved', 'skipped', 'all'] as const

export default function RunDetailPage() {
  const { id } = useParams()
  const [run, setRun] = useState<Run | null>(null)
  const [images, setImages] = useState<Image[]>([])
  const [filter, setFilter] = useState<(typeof FILTERS)[number]>('saved')
  const [page, setPage] = useState(1)
  const [limit, setLimit] = useState(60)
  const [total, setTotal] = useState(0)
  const [selected, setSelected] = useState<Image | null>(null)
  const [similar, setSimilar] = useState<Similar[]>([])
  const [simLoading, setSimLoading] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    fetch(`/api/runs/${id}`)
      .then((r) => r.json())
      .then((d) => setRun(d.run ?? null))
      .catch((e) => setError(String(e)))
  }, [id])

  useEffect(() => {
    fetch(`/api/runs/${id}/images?skipped=${filter}&page=${page}&limit=${limit}`)
      .then((r) => r.json())
      .then((d) => {
        setImages(d.images ?? [])
        setTotal(d.total ?? 0)
      })
      .catch((e) => setError(String(e)))
  }, [id, filter, page, limit])

  if (error) {
    return <p className="text-sm text-red-500">{error}</p>
  }
  if (!run) {
    return <p className="text-sm text-muted-foreground">Loading…</p>
  }

  const openImage = (img: Image) => {
    setSelected(img)
    setSimilar([])
    setSimLoading(true)
    fetch(`/api/images/${img.id}/similar`)
      .then((r) => r.json())
      .then((d) => {
        setSimilar(d.images ?? [])
        setSimLoading(false)
      })
      .catch(() => setSimLoading(false))
  }

  return (
    <div className="space-y-4">
      <Link to="/runs" className="text-sm text-primary hover:underline">
        ← All runs
      </Link>
      <h1 className="text-xl font-semibold">{run.domain}</h1>

      <div className="grid grid-cols-2 gap-3 md:grid-cols-6">
        <Stat label="Started" value={run.started_at} />
        <Stat label="Duration" value={`${run.duration_s.toFixed(0)}s`} />
        <Stat label="Depth" value={String(run.depth)} />
        <Stat label="Pages visited" value={String(run.pages_visited)} />
        <Stat label="Images downloaded" value={String(run.images_downloaded)} />
      </div>

      <div className="flex items-center gap-2">
        {FILTERS.map((f) => (
          <button
            key={f}
            onClick={() => {
              setFilter(f)
              setPage(1)
            }}
            className={`rounded-md px-3 py-1 text-sm transition-colors ${
              filter === f ? 'bg-primary text-primary-foreground' : 'bg-muted text-foreground hover:bg-muted/70'
            }`}
          >
            {f}
          </button>
        ))}
        <span className="ml-auto text-sm text-muted-foreground">{total.toLocaleString()} images</span>
      </div>

      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-4">
        {images.map((img) => (
          <Card
            key={img.id}
            className={img.skipped ? '' : 'cursor-pointer transition-colors hover:border-primary'}
            onClick={img.skipped ? undefined : () => openImage(img)}
          >
            <CardContent className="p-3">
              {img.skipped ? (
                <div className="flex h-32 items-center justify-center rounded bg-muted p-2 text-center text-xs text-muted-foreground">
                  skipped: {img.skipped}
                </div>
              ) : (
                <a href={`/media/${img.saved_path}`} target="_blank" rel="noreferrer">
                  <img
                    src={`/media/${img.saved_path}`}
                    alt={img.url}
                    loading="lazy"
                    className="h-32 w-full rounded object-cover"
                  />
                </a>
              )}
              <div className="mt-2 space-y-0.5 text-xs text-muted-foreground">
                <div>
                  {img.width > 0 ? `${img.width}×${img.height}` : '—'} · {img.ext || '—'}
                  {img.size_bytes > 0 && ` · ${(img.size_bytes / 1048576).toFixed(2)} MB`}
                </div>
                <div className="truncate" title={img.url}>
                  {img.url}
                </div>
              </div>
            </CardContent>
          </Card>
        ))}
        {images.length === 0 && (
          <div className="col-span-full py-8 text-center text-sm text-muted-foreground">No images here.</div>
        )}
      </div>

      <Pagination
        page={page}
        limit={limit}
        total={total}
        onPage={setPage}
        onLimit={(l) => {
          setLimit(l)
          setPage(1)
        }}
        label="images"
        pageSizes={[24, 60, 120, 240]}
      />

      {selected && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
          onClick={() => setSelected(null)}
        >
          <div
            className="max-h-[90vh] w-full max-w-4xl overflow-auto rounded-lg border bg-card p-4"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-start justify-between gap-4">
              <h2 className="truncate text-sm font-semibold" title={selected.url}>
                {selected.url}
              </h2>
              <button
                onClick={() => setSelected(null)}
                className="rounded px-2 py-0.5 text-muted-foreground hover:bg-muted"
              >
                ✕
              </button>
            </div>
            <img
              src={`/media/${selected.saved_path}`}
              alt={selected.url}
              className="mx-auto mt-2 max-h-[50vh] rounded object-contain"
            />
            <div className="mt-2 space-y-0.5 text-xs text-muted-foreground">
              <div>
                {selected.width}×{selected.height} px · {selected.ext} ·{' '}
                {(selected.size_bytes / 1048576).toFixed(2)} MB · saved as{' '}
                <code className="rounded bg-muted px-1">{selected.saved_path}</code>
              </div>
              <div>
                source <a className="text-primary hover:underline" href={selected.url} target="_blank" rel="noreferrer">{selected.url}</a>
              </div>
            </div>

            <h3 className="mt-4 text-sm font-semibold">Similar images</h3>
            {simLoading ? (
              <p className="mt-2 text-sm text-muted-foreground">Loading…</p>
            ) : similar.length === 0 ? (
              <p className="mt-2 text-sm text-muted-foreground">No similar images found.</p>
            ) : (
              <div className="mt-2 grid grid-cols-3 gap-3 md:grid-cols-4">
                {similar.map((s) => (
                  <button key={s.id} onClick={() => openImage(s)} className="group text-left">
                    <img
                      src={`/media/${s.saved_path}`}
                      alt={s.url}
                      className="h-24 w-full rounded object-cover"
                    />
                    <div className="mt-1 truncate text-xs text-muted-foreground" title={s.url}>
                      {s.url}
                    </div>
                    <div className="text-xs">distance {s.distance}</div>
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
