//go:build !windows

package pbscommon

import "os"

// platformEntryAttrs off Windows: there are no attributes or reparse tags, so a
// symbolic link is reported as the Windows symlink it corresponds to and every
// other entry as plain. The client ships only for Windows; this keeps the
// writer's behavior on the Linux test and build hosts the same shape.
func platformEntryAttrs(_ string, fi os.FileInfo) (uint32, uint32, error) {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fileAttributeReparsePoint, ioReparseTagSymlink, nil
	}
	if fi.IsDir() {
		return fileAttributeDirectory, 0, nil
	}
	return 0, 0, nil
}
