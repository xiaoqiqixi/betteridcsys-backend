package paymentv1

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
	"unicode/utf8"
)

// CallbackAcknowledgement is emitted only after Core durably accepts the
// verified settlement (including an exact replay). A nil acknowledgement keeps
// Core's default JSON response. Plugins cannot supply cookies or other headers.
type CallbackAcknowledgement struct {
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body,omitempty"`
}

func (a *CallbackAcknowledgement) Validate() error {
	if a == nil {
		return nil
	}
	if a.StatusCode == 204 && a.ContentType == "" && a.Body == "" {
		return nil
	}
	if a.StatusCode != 200 || len(a.Body) > 4096 || !utf8.ValidString(a.Body) || strings.ContainsRune(a.Body, '\x00') {
		return ErrInvalid
	}
	switch a.ContentType {
	case "text/plain":
		return nil
	case "application/json":
		if json.Valid([]byte(a.Body)) {
			return nil
		}
	case "application/xml", "text/xml":
		decoder := xml.NewDecoder(strings.NewReader(a.Body))
		depth, roots := 0, 0
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				if depth == 0 && roots == 1 {
					return nil
				}
				break
			}
			if err != nil {
				break
			}
			switch v := token.(type) {
			case xml.StartElement:
				if depth == 0 {
					roots++
				}
				depth++
				if roots > 1 || depth > 16 {
					return ErrInvalid
				}
			case xml.EndElement:
				depth--
			case xml.CharData:
				if depth == 0 && strings.TrimSpace(string(v)) != "" {
					return ErrInvalid
				}
			case xml.Directive:
				return ErrInvalid // No DTDs or entity declarations.
			case xml.ProcInst:
				if v.Target != "xml" || roots != 0 {
					return ErrInvalid // No stylesheets or executable transforms.
				}
			}
		}
	}
	return ErrInvalid
}
