import { createRoot } from 'react-dom/client'
import App from './App'
import { TooltipProvider } from './design-system'
import './styles.css'

const root = document.getElementById('root')
if (!root) throw new Error('Carina Harness root element is missing')
createRoot(root).render(
  <TooltipProvider delayDuration={300} skipDelayDuration={100}>
    <App />
  </TooltipProvider>,
)
