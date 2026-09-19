package pbscommon

import (
	"strings"
	"testing"
)

func TestSessionErrorDoesNotMislabelStorageFailure(t *testing.T) {
	for _, code := range []string{"400 Bad Request", "500 Internal Server Error", "503 Service Unavailable"} {
		e := &PBSResponseError{StatusCode: code, ResponseBody: "backup group unavailable"}
		if strings.Contains(e.Error(), "authentication") || !strings.Contains(e.Error(), code) {
			t.Fatalf("misleading error: %v", e)
		}
	}
	for _, code := range []string{"401 Unauthorized", "403 Forbidden"} {
		if !strings.Contains((&PBSResponseError{StatusCode: code}).Error(), "authentication or authorization") {
			t.Fatal("auth failure not identified")
		}
	}
}
