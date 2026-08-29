import { Navigate, Route, Routes, useParams } from 'react-router-dom'
import { AppShell } from '@/components/AppShell'
import { KBsPage } from '@/pages/KBsPage'
import { KBDetailPage } from '@/pages/KBDetailPage'
import { PrivacyPage } from '@/pages/PrivacyPage'

// v4.5: Documents and Search merged into one page at /kbs/:kbId. This keeps
// any bookmarked/shared /search link working instead of 404ing.
function RedirectToKBDetail() {
  const { kbId } = useParams<{ kbId: string }>()
  return <Navigate to={`/kbs/${kbId}`} replace />
}

export default function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<Navigate to="/kbs" replace />} />
        <Route path="/kbs" element={<KBsPage />} />
        <Route path="/kbs/:kbId" element={<KBDetailPage />} />
        <Route path="/kbs/:kbId/search" element={<RedirectToKBDetail />} />
        <Route path="/privacy" element={<PrivacyPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/kbs" replace />} />
    </Routes>
  )
}
