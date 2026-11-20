import { createRoot } from 'react-dom/client'
import App from './App'
import './observe.css'

document.documentElement.classList.add('dark')
createRoot(document.getElementById('app')!).render(<App />)
