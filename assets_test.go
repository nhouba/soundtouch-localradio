package localradio

import (
	"io/fs"
	"testing"

	"github.com/nhouba/soundtouch-localradio/internal/fileserver"
)

func TestSpeakerFilesEmbedded(t *testing.T) {
	for _, f := range fileserver.Served {
		b, err := fs.ReadFile(SpeakerFiles(), f)
		if err != nil || len(b) == 0 {
			t.Errorf("%s not embedded: %v", f, err)
		}
	}
}
