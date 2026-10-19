import { useEffect, useState } from 'react'
import Dropdown from './Dropdown'
import { useTranslation } from '../i18n/i18nContext'

const THEME_KEY = 'nimbus.theme'
const ACCENT_KEY = 'nimbus.accent'
const FONTSIZE_KEY = 'nimbus.fontsize'

/** Apply base theme to <html>. 'auto' removes the attribute so the OS-media-
 *  query rules in index.css take over. Exported so App.jsx's initial mount
 *  can reuse the same logic as index.html's pre-paint bootstrap. */
export function applyTheme(choice) {
  const root = document.documentElement
  if (choice && choice !== 'auto') root.setAttribute('data-theme', choice)
  else root.removeAttribute('data-theme')
}

/** Apply accent overlay. 'orange' is the default (built into each base mode),
 *  so it is represented by REMOVING the attribute. */
export function applyAccent(accent) {
  const root = document.documentElement
  if (accent && accent !== 'orange') root.setAttribute('data-accent', accent)
  else root.removeAttribute('data-accent')
}

export function applyFontSize(size) {
  document.documentElement.setAttribute('data-fontsize', size || 'small')
}

/**
 * HeaderControls — Theme, Font Size, and Language: three dropdowns, same
 * component, same row. The Theme dropdown holds ONLY the base modes
 * (Auto/Light/Dark); accent is a separate swatch row inside that dropdown's
 * menu (orange default + pink/forest/sky). Accent layers on top of the base
 * mode — dark base gets muted accents, light base brighter ones — so it
 * never overpowers the theme. Picking a base mode does not clear the accent;
 * picking the orange swatch returns to default.
 */
export default function HeaderControls() {
  const { language, setLanguage, t } = useTranslation()

  const [theme, setThemeState] = useState(() => localStorage.getItem(THEME_KEY) || 'auto')
  const [accent, setAccentState] = useState(() => localStorage.getItem(ACCENT_KEY) || 'orange')
  const [fontSize, setFontSizeState] = useState(() => localStorage.getItem(FONTSIZE_KEY) || 'small')

  useEffect(() => { applyTheme(theme) }, [theme])
  useEffect(() => { applyAccent(accent) }, [accent])
  useEffect(() => { applyFontSize(fontSize) }, [fontSize])

  const setTheme = (v) => {
    setThemeState(v)
    if (v === 'auto') localStorage.removeItem(THEME_KEY)
    else localStorage.setItem(THEME_KEY, v)
  }
  const setAccent = (v) => {
    setAccentState(v)
    if (v === 'orange') localStorage.removeItem(ACCENT_KEY)
    else localStorage.setItem(ACCENT_KEY, v)
  }
  const setFontSize = (v) => {
    setFontSizeState(v)
    localStorage.setItem(FONTSIZE_KEY, v)
  }

  // Base modes only — accents are the swatch row below (footer slot).
  const themeOptions = [
    { value: 'auto',  label: t('themeAuto'),  icon: '💻' },
    { value: 'light', label: t('themeLight'), icon: '☀️' },
    { value: 'dark',  label: t('themeDark'),  icon: '🌙' },
  ]

  // The swatch row rendered inside the Theme dropdown menu.
  const accents = [
    { value: 'orange', label: t('accentDefault'), color: '#e57000' },
    { value: 'pink',   label: t('themePink'),     color: '#c8577e' },
    { value: 'forest', label: t('themeForest'),   color: '#3f7d52' },
    { value: 'sky',    label: t('themeSky'),      color: '#2f93b8' },
  ]
  const accentSwatches = (
    <div className="nc-accent-row">
      <div className="nc-accent-label">{t('accentLabel')}</div>
      <div className="nc-swatches">
        {accents.map(a => (
          <button
            key={a.value}
            type="button"
            className={`nc-swatch ${accent === a.value ? 'selected' : ''}`}
            style={{ background: a.color }}
            title={a.label}
            aria-label={a.label}
            aria-pressed={accent === a.value}
            onClick={() => setAccent(a.value)}
          />
        ))}
      </div>
    </div>
  )

  const fontSizeOptions = [
    { value: 'small',  label: t('fontSizeSmall'),  icon: <span className="nc-a-icon nc-a-small">A</span> },
    { value: 'medium', label: t('fontSizeMedium'), icon: <span className="nc-a-icon nc-a-medium">A</span> },
    { value: 'large',  label: t('fontSizeLarge'),  icon: <span className="nc-a-icon nc-a-large">A</span> },
  ]

  const languageOptions = [
    { value: 'fr', label: 'Français', icon: '🇫🇷' },
    { value: 'en', label: 'English',  icon: '🇬🇧' },
    { value: 'es', label: 'Español',  icon: '🇪🇸' },
  ]

  return (
    <div className="header-controls">
      <Dropdown
        value={theme}
        options={themeOptions}
        onChange={setTheme}
        ariaLabel={t('themeLabel')}
        footer={accentSwatches}
      />
      <Dropdown
        value={fontSize}
        options={fontSizeOptions}
        onChange={setFontSize}
        ariaLabel={t('fontSizeLabel')}
        renderTrigger={(sel) => sel?.icon}
      />
      <Dropdown
        value={language}
        options={languageOptions}
        onChange={setLanguage}
        ariaLabel={t('languageLabel')}
      />
    </div>
  )
}
