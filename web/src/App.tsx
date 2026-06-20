import { Navigate, Route, Routes } from 'react-router-dom'
import { useAuth } from '@/context/AuthContext'
import { AppShell } from '@/components/AppShell'
import { LoginPage } from '@/pages/LoginPage'
import { RegisterPage } from '@/pages/RegisterPage'
import { KBsPage } from '@/pages/KBsPage'
import { KBDetailPage } from '@/pages/KBDetailPage'
import { SearchPage } from '@/pages/SearchPage'

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, isLoaded } = useAuth()
  // Wait for Clerk to resolve the initial session before deciding whether
  // to redirect, so a signed-in user isn't bounced to /login during the
  // brief window before the session loads.
  if (!isLoaded) return null
  return isAuthenticated ? <>{children}</> : <Navigate to="/login" replace />
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/register" element={<RegisterPage />} />
      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route index element={<Navigate to="/kbs" replace />} />
        <Route path="/kbs" element={<KBsPage />} />
        <Route path="/kbs/:kbId" element={<KBDetailPage />} />
        <Route path="/kbs/:kbId/search" element={<SearchPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/kbs" replace />} />
    </Routes>
  )
}
