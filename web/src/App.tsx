import { Navigate, Route, Routes } from 'react-router-dom'
import { AppShell } from '@/components/AppShell'
import { KBsPage } from '@/pages/KBsPage'
import { KBDetailPage } from '@/pages/KBDetailPage'
import { SearchPage } from '@/pages/SearchPage'
import { PrivacyPage } from '@/pages/PrivacyPage'

export default function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<Navigate to="/kbs" replace />} />
        <Route path="/kbs" element={<KBsPage />} />
        <Route path="/kbs/:kbId" element={<KBDetailPage />} />
        <Route path="/kbs/:kbId/search" element={<SearchPage />} />
        <Route path="/privacy" element={<PrivacyPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/kbs" replace />} />
    </Routes>
  )
}
