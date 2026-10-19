import StatusPanel from './StatusPanel'
import StorageIdentityPanel from '../../components/StorageIdentityPanel'
import OrgSuspendedBanner from '../frame/OrgSuspendedBanner'
import ReleaseBanners from '../frame/ReleaseBanners'

// A locked console: the status page and the storage identity, read-only.
export default function LockedView({ cpStatus, version, hostname }) {
  return <><OrgSuspendedBanner cpStatus={cpStatus} /><ReleaseBanners status={cpStatus} /><StatusPanel version={version} hostname={hostname} /><StorageIdentityPanel status={cpStatus?.storage_identity} readOnly /></>
}
