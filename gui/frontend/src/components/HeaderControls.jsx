import { useEffect, useState } from 'react'
import Dropdown from './Dropdown'
import { useTranslation } from '../i18n/i18nContext'

const THEME_KEY = 'nimbus.theme'
const FONTSIZE_KEY = 'nimbus.fontsize'

/** Apply theme to <html>. 'auto' removes the attribute so the OS-media-query
 *  rules in index.css take over. Exported so App.jsx's initial mount can
 *  reuse the exact same logic as index.html's pre-paint bootstrap. */
export function applyTheme(choice) {
  const root = document.documentElement
  if (choice && choice !== 'auto') root.setAttribute('data-theme', choice)
  else root.removeAttribute('data-theme')
}

export function applyFontSize(size) {
  document.documentElement.setAttribute('data-fontsize', size || 'small')
}

/**
 * HeaderControls — Theme, Font Size, and Language: three dropdowns, same
 * component, same row, same visual layer. Replaces the old absolutely-
 * positioned theme toggle (which visually overlapped the language
 * switcher) and the flag-button language switcher (different element type
 * from everything else).
 */
export default function HeaderControls() {
  const { language, setLanguage, t } = useTranslation()

  const [theme, setThemeState] = useState(() => localStorage.getItem(THEME_KEY) || 'auto')
  const [fontSize, setFontSizeState] = useState(() => localStorage.getItem(FONTSIZE_KEY) || 'small')

  // Re-apply on mount too: index.html already set the attributes before
  // paint, but this keeps React state and the DOM attribute guaranteed in
  // sync (e.g. after a hot reload during development).
  useEffect(() => { applyTheme(theme) }, [theme])
  useEffect(() => { applyFontSize(fontSize) }, [fontSize])

  const setTheme = (v) => {
    setThemeState(v)
    if (v === 'auto') localStorage.removeItem(THEME_KEY)
    else localStorage.setItem(THEME_KEY, v)
  }
  const setFontSize = (v) => {
    setFontSizeState(v)
    localStorage.setItem(FONTSIZE_KEY, v)
  }

  const themeOptions = [
    { value: 'auto',  label: t('themeAuto'),  icon: '💻', group: '' },
    { value: 'light', label: t('themeLight'), icon: '☀️', group: '' },
    { value: 'dark',  label: t('themeDark'),  icon: '🌙', group: '' },
    { value: 'pink',   label: t('themePink'),   icon: '🌸', group: t('themeAdvancedGroup') },
    { value: 'forest', label: t('themeForest'), icon: '🌲', group: t('themeAdvancedGroup') },
    { value: 'sky',    label: t('themeSky'),    icon: '🌤️', group: t('themeAdvancedGroup') },
  ]

  // The "A" glyph itself IS the size preview — literally shown at each
  // option's relative size (small/medium/large) as requested.
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
