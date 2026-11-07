import { useEffect, useState } from 'react'

interface Category {
  id: number
  kind: string
  name: string
  url: string
  parent_id: number
}

interface TreeNode {
  name: string
  url: string
  children: TreeNode[]
}

// buildTree merges both kinds into ONE tree: nodes with the same name are
// combined (children unioned, deduped by name), roots are unioned by name,
// and the two synthetic kind groups are dropped. Design roots keep their
// order, photo-only roots are appended.
function buildTree(flat: Category[]): TreeNode[] {
  const byKind = new Map<string, Map<string, TreeNode>>()
  const rootOrder = new Map<string, string[]>()
  const nodes = new Map<number, TreeNode>()
  for (const c of flat) {
    const node: TreeNode = { name: c.name, url: c.url, children: [] }
    nodes.set(c.id, node)
    const km = byKind.get(c.kind) ?? new Map()
    byKind.set(c.kind, km)
    if (c.parent_id === 0) {
      km.set(c.name, node)
      const order = rootOrder.get(c.kind) ?? []
      order.push(c.name)
      rootOrder.set(c.kind, order)
    } else {
      const parent = nodes.get(c.parent_id)
      if (parent) parent.children.push(node)
    }
  }

  const mergedRoots: TreeNode[] = []
  const merged = new Map<string, TreeNode>()
  for (const kind of ['design', 'photo']) {
    const km = byKind.get(kind)
    if (!km) continue
    for (const name of rootOrder.get(kind) ?? []) {
      const node = km.get(name)
      if (!node) continue
      const existing = merged.get(name)
      if (existing) {
        mergeChildren(existing, node)
      } else {
        const copy: TreeNode = { name: node.name, url: node.url, children: [...node.children] }
        merged.set(name, copy)
        mergedRoots.push(copy)
      }
    }
  }
  return mergedRoots
}

// mergeChildren unions src's children into dst, recursing one level so
// deeper trees (e.g. merged root -> shared child -> grandchildren) combine.
function mergeChildren(dst: TreeNode, src: TreeNode) {
  for (const sc of src.children) {
    const existing = dst.children.find((dc) => dc.name === sc.name)
    if (existing) {
      for (const gc of sc.children) {
        if (!existing.children.some((e) => e.name === gc.name)) existing.children.push(gc)
      }
    } else {
      dst.children.push(sc)
    }
  }
}

function TreeNodeView({ node, depth }: { node: TreeNode; depth: number }) {
  if (node.children.length === 0) {
    return (
      <a
        href={node.url}
        target="_blank"
        rel="noreferrer"
        className="block truncate rounded px-1 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
        title={node.name}
      >
        {node.name}
      </a>
    )
  }
  return (
    <details className="rounded-md bg-muted/50 p-2" open={depth === 0}>
      <summary className="cursor-pointer text-sm font-medium">
        {node.name}
        <span className="ml-1 text-xs text-muted-foreground">({node.children.length})</span>
      </summary>
      <div className="mt-2 space-y-1 pl-2">
        {node.children.map((c) => (
          <TreeNodeView key={c.url || c.name} node={c} depth={depth + 1} />
        ))}
      </div>
    </details>
  )
}

function TreeColumn({ title, roots }: { title: string; roots: TreeNode[] }) {
  return (
    <div className="rounded-lg border bg-card">
      <div className="border-b p-3 text-sm font-semibold">{title}</div>
      <div className="max-h-[70vh] space-y-2 overflow-auto p-3">
        {roots.length === 0 && <p className="text-xs text-muted-foreground">Empty — run image-harvest -categories.</p>}
        {roots.map((r) => (
          <TreeNodeView key={r.url || r.name} node={r} depth={0} />
        ))}
      </div>
    </div>
  )
}

export default function CategoriesPage() {
  const [tree, setTree] = useState<TreeNode[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    fetch('/api/categories')
      .then((r) => r.json())
      .then((d) => setTree(buildTree(d.categories ?? [])))
      .catch((e) => setError(String(e)))
  }, [])

  if (error) {
    return <p className="text-sm text-red-500">{error}</p>
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Category tree</h1>
      <p className="text-sm text-muted-foreground">
        Merged taxonomy from nipic.com's design and photo sidebars. Run{' '}
        <code className="rounded bg-muted px-1">image-harvest -categories</code> to refresh.
      </p>
      <TreeColumn title="All categories" roots={tree} />
    </div>
  )
}
