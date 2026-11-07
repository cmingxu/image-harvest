import { Navigate, NavLink, Route, Routes } from 'react-router-dom'
import { BarChart3, Image as ImageIcon, Search, FolderTree, Database } from 'lucide-react'

import { cn } from './lib/utils'
import RunsPage from './pages/RunsPage'
import RunDetailPage from './pages/RunDetailPage'
import DiscoverPage from './pages/DiscoverPage'
import CategoriesPage from './pages/CategoriesPage'
import MetadataPage from './pages/MetadataPage'

const sidebarSections = [
  {
    title: 'Harvest',
    items: [{ to: '/runs', label: 'Runs', icon: BarChart3 }],
  },
  {
    title: 'Explore',
    items: [
      { to: '/discover', label: 'Discover', icon: Search },
      { to: '/categories', label: 'Categories', icon: FolderTree },
    ],
  },
  {
    title: 'System',
    items: [{ to: '/metadata', label: 'Metadata', icon: Database }],
  },
]

function AppLayout() {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="flex h-14 items-center justify-between border-b bg-card px-4">
        <div className="flex items-center gap-3">
          <div className="flex h-8 w-8 items-center justify-center rounded-md bg-primary/10">
            <ImageIcon className="h-4 w-4 text-primary" />
          </div>
          <div className="leading-tight">
            <div className="text-sm font-semibold">Image Harvest</div>
            <div className="text-xs text-muted-foreground">Crawl · Collect · Browse</div>
          </div>
        </div>
      </header>

      <div className="flex min-h-[calc(100vh-3.5rem)]">
        <aside className="flex w-64 shrink-0 flex-col border-r bg-card p-4">
          <nav className="space-y-6">
            {sidebarSections.map((section) => (
              <div key={section.title}>
                <h4 className="mb-2 px-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  {section.title}
                </h4>
                <div className="space-y-1">
                  {section.items.map((it) => (
                    <NavLink
                      key={it.to}
                      to={it.to}
                      className={({ isActive }) =>
                        cn(
                          'flex items-center gap-2 rounded-md px-3 py-2 text-sm transition-colors',
                          isActive
                            ? 'bg-primary font-medium text-primary-foreground shadow-sm'
                            : 'text-foreground hover:bg-muted',
                        )
                      }
                    >
                      <it.icon className="h-4 w-4" />
                      <span>{it.label}</span>
                    </NavLink>
                  ))}
                </div>
              </div>
            ))}
          </nav>
        </aside>

        <main className="flex min-h-[calc(100vh-3.5rem)] flex-1 flex-col bg-slate-50 p-6">
          <div className="flex-1">
            <Routes>
              <Route path="/" element={<Navigate to="/runs" replace />} />
              <Route path="/runs" element={<RunsPage />} />
              <Route path="/runs/:id" element={<RunDetailPage />} />
              <Route path="/discover" element={<DiscoverPage />} />
              <Route path="/categories" element={<CategoriesPage />} />
              <Route path="/metadata" element={<MetadataPage />} />
              <Route path="*" element={<Navigate to="/runs" replace />} />
            </Routes>
          </div>
          <footer className="mt-8 border-t pt-4 text-xs text-muted-foreground">
            &copy; {new Date().getFullYear()} Image Harvest
          </footer>
        </main>
      </div>
    </div>
  )
}

export default function App() {
  return <AppLayout />
}
