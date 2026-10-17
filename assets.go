// Package localradio holds the files that the installer copies onto the speaker.
package localradio

import (
	"embed"
	"io/fs"
)

//go:embed speaker
var speakerFS embed.FS

// SpeakerFiles returns the contents of the repository's speaker/ directory
// (registry.json, index.html, cgi-bin/s, cgi-bin/api), rooted at that directory.
func SpeakerFiles() fs.FS {
	sub, err := fs.Sub(speakerFS, "speaker")
	if err != nil {
		panic(err) // cannot happen: "speaker" is a valid, embedded path
	}
	return sub
}
