import StatusPanel from './StatusPanel'
import StorageIdentityPanel from '../../components/StorageIdentityPanel'
import OrgSuspendedBanner from '../frame/OrgSuspendedBanner'

// A locked console: the status page and the storage identity, read-only.
export default function LockedView({ cpStatus, version, hostname }) {
  return <><OrgSuspendedBanner cpStatus={cpStatus} /><StatusPanel version={version} hostname={hostname} /><StorageIdentityPanel status={cpStatus?.storage_identity} readOnly /></>
}
