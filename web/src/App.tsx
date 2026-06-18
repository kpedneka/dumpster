import { Navigate, Route, Routes } from 'react-router-dom'
import { useAuth } from '@/context/AuthContext'
import { AppShell } from '@/components/AppShell'
import { LoginPage } from '@/pages/LoginPage'
import { RegisterPage } from '@/pages/RegisterPage'
import { KBsPage } from '@/pages/KBsPage'
import { KBDetailPage } from '@/pages/KBDetailPage'

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { isAuthenticated } = useAuth()
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
      </Route>
      <Route path="*" element={<Navigate to="/kbs" replace />} />
    </Routes>
  )
}
