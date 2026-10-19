import { useTranslation } from '../../i18n/i18nContext'

// Release line and self-update (NimbusControl V4-CLIENT-RELEASES §1.4, §7.2):
// one line each, from the service's control-plane status.
export default function ReleaseBanners({ status }) {
  const { t } = useTranslation()
  if (!status) return null
  return <>
    {status.update_pending && <p className="card" role="status">{t('updatePendingBanner').replace('{version}', status.update_pending)}</p>}
    {status.agent_line === 'older' && <p className="card" role="alert">{t('agentLineOlderBanner').replace('{version}', status.agent_line_needs || '')}</p>}
    {status.agent_line === 'newer' && <p className="card" role="alert">{t('agentLineNewerBanner').replace('{version}', status.agent_line_needs || '')}</p>}
  </>
}
