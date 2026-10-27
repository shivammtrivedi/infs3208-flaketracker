import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { ApiError } from './api/client'
import App from './App.tsx'
import './index.css'

// One QueryClient holds the cache for every query in the app.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Retry a failed request once, but only when retrying could help: a
      // 503 (a service restarting) or a network blip. A 4xx means the request
      // was wrong or the thing doesn't exist, so show that error immediately.
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false
        return failureCount < 1
      },
      // Data counts as fresh for 5 s, so quickly switching between pages
      // doesn't refetch everything every time.
      staleTime: 5_000,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
