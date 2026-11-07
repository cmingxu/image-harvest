import { ChevronLeft, ChevronRight } from 'lucide-react'

interface PaginationProps {
  page: number
  limit: number
  total: number
  onPage: (page: number) => void
  onLimit?: (limit: number) => void
  label?: string
  pageSizes?: number[]
}

const num = (n: number) => n.toLocaleString()

/** Compressed page list: 1 … 4 5 [6] 7 8 … 42 */
function pageWindow(page: number, pages: number): (number | '…')[] {
  if (pages <= 7) return Array.from({ length: pages }, (_, i) => i + 1)
  const out: (number | '…')[] = [1]
  const start = Math.max(2, page - 1)
  const end = Math.min(pages - 1, page + 1)
  if (start > 2) out.push('…')
  for (let p = start; p <= end; p++) out.push(p)
  if (end < pages - 1) out.push('…')
  out.push(pages)
  return out
}

export default function Pagination({ page, limit, total, onPage, onLimit, label = 'records', pageSizes = [25, 50, 100, 200] }: PaginationProps) {
  const pages = Math.max(1, Math.ceil(total / limit))
  if (total === 0) return null

  const first = (page - 1) * limit + 1
  const last = Math.min(page * limit, total)

  const btn = 'flex h-8 min-w-8 items-center justify-center rounded-md border bg-card px-2 text-sm transition-colors disabled:opacity-40 disabled:cursor-not-allowed enabled:hover:bg-muted'

  return (
    <div className="flex flex-wrap items-center justify-between gap-3 pt-2">
      <p className="text-xs text-muted-foreground">
        {num(first)}–{num(last)} of {num(total)} {label} · page {num(page)} of {num(pages)}
      </p>
      <div className="flex items-center gap-3">
        {onLimit && (
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            per page
            <select
              className="h-8 rounded-md border bg-card px-2 text-sm text-foreground"
              value={limit}
              onChange={(e) => onLimit(Number(e.target.value))}
            >
              {pageSizes.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>
        )}
        <div className="flex items-center gap-1">
          <button className={btn} onClick={() => onPage(page - 1)} disabled={page <= 1} aria-label="Previous page">
            <ChevronLeft className="h-4 w-4" />
          </button>
          {pageWindow(page, pages).map((p, i) =>
            p === '…' ? (
              <span key={`gap-${i}`} className="px-1 text-xs text-muted-foreground">
                …
              </span>
            ) : (
              <button
                key={p}
                onClick={() => onPage(p)}
                className={`${btn} ${p === page ? 'border-primary bg-primary text-primary-foreground enabled:hover:bg-primary' : ''}`}
              >
                {num(p)}
              </button>
            ),
          )}
          <button className={btn} onClick={() => onPage(page + 1)} disabled={page >= pages} aria-label="Next page">
            <ChevronRight className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  )
}
