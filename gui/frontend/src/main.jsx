import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
// OpenDyslexic (OFL-1.1), bundled locally via @fontsource — used globally at
// all three font-size settings (see index.css --nc-font). No CDN, no
// runtime network dependency; ships inside the app bundle. Declared in
// fonts.css, WOFF2 only (that file says why).
import './fonts.css'
import './index.css'
import { I18nProvider } from './i18n/i18nContext'

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <I18nProvider>
      <App />
    </I18nProvider>
  </React.StrictMode>,
)
