import { createRoot } from 'react-dom/client'
import App from './App'
import { startObserverTelemetry } from './telemetry'
import './observe.css'

document.documentElement.classList.add('dark')
startObserverTelemetry()
createRoot(document.getElementById('app')!).render(<App />)
