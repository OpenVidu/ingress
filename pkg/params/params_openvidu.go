// BEGIN OPENVIDU BLOCK

package params

import "net/url"

// redactURLUserinfo hides the password of a pull URL that carries credentials
// (rtsp://user:secret@camera/stream), as IP cameras usually do. The service
// logs the whole IngressInfo on every start, and the redaction upstream applies
// covers only the stream key. The username stays: it tells which account a
// camera was pulled with, and gives nothing away. A URL that does not parse,
// or carries no password, is returned as it came.
func redactURLUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return raw
	}
	return u.Redacted()
}

// END OPENVIDU BLOCK
