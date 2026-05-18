import React from 'react'
import ReactDOM from 'react-dom/client'
import { applyTheme, getInitialTheme } from '@hollis-labs/sysop-ui'
import { App } from './App'
import './index.css'

applyTheme(getInitialTheme())

const root = document.getElementById('root')
if (!root) {
  throw new Error('root element #root not found')
}

ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
