import { buildTree } from '../../lib/snapshots'
import TreeNode from './TreeNode'

// A file backup's tree, from the flat entry list.
export default function FileTree({ r }) {
  const tree = buildTree(r.snapshotEntries)
  const roots = tree.get('') || []
  return roots.map(e => <TreeNode key={e.path} r={r} entry={e} childrenByDir={tree} depth={0} />)
}
