import { useTranslation } from '../../i18n/i18nContext'

// F-22: the organization is suspended; scheduled backups do not run.
export default function OrgSuspendedBanner({ cpStatus }) {
  const { t } = useTranslation()
  if (!cpStatus?.org_suspended) return null
  return <p className="card" role="alert">{t('orgSuspendedBanner')}</p>
}
