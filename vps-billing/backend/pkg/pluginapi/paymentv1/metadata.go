package paymentv1

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"image/png"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

type CurrencyAmount struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}
type Fee struct {
	PercentBP int              `json:"percent_bp"`
	Fixed     []CurrencyAmount `json:"fixed"`
}

var gatewayID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func ValidGatewayID(s string) bool { return gatewayID.MatchString(s) }
func (d Declarations) CheckoutFlow() string {
	if d.Flow == "" {
		return "redirect"
	}
	return d.Flow
}
func PlainText(s string, max int, multiline bool) bool {
	if len(s) == 0 || utf8.RuneCountInString(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c == 127 || c < 32 && !(multiline && (c == '\n' || c == '\t')) {
			return false
		}
	}
	return true
}
func ValidateLocalized(value map[string]string, max int) error {
	if len(value) > 32 {
		return ErrInvalid
	}
	for locale, text := range value {
		if locale != "en" && locale != "zh-CN" || !PlainText(text, max, true) {
			return ErrInvalid
		}
	}
	return nil
}
func (d Declarations) ValidateMetadata() error {
	if d.CheckoutFlow() != "redirect" && d.Flow != "qr" && d.Flow != "instructions" {
		return ErrInvalid
	}
	if d.Flow != "" && len(d.Name) == 0 || ValidateLocalized(d.Name, 4000) != nil || ValidateLocalized(d.Description, 4000) != nil {
		return ErrInvalid
	}
	if d.Logo != "" && ValidateLogo(d.Logo) != nil {
		return ErrInvalid
	}
	groups := [][]CurrencyAmount{d.Min, d.Max}
	if d.Fee != nil {
		if d.Fee.PercentBP < 0 || d.Fee.PercentBP > 10000 {
			return ErrInvalid
		}
		groups = append(groups, d.Fee.Fixed)
	}
	for _, group := range groups {
		seen := map[string]bool{}
		for _, item := range group {
			found := false
			for _, c := range d.Currencies {
				if c.Code == item.Currency {
					found = true
				}
			}
			if !found || seen[item.Currency] || item.AmountMinor < 0 || item.AmountMinor > MaxMinor {
				return ErrInvalid
			}
			seen[item.Currency] = true
		}
	}
	for _, min := range d.Min {
		for _, max := range d.Max {
			if min.Currency == max.Currency && min.AmountMinor > max.AmountMinor {
				return ErrInvalid
			}
		}
	}
	return nil
}
func (d Declarations) WithinLimits(currency string, amount int64) bool {
	for _, min := range d.Min {
		if min.Currency == currency && amount < min.AmountMinor {
			return false
		}
	}
	for _, max := range d.Max {
		if max.Currency == currency && amount > max.AmountMinor {
			return false
		}
	}
	return true
}

// Logos are passive, bounded images. SVG uses an allow-list: no scripts, CSS,
// entities, links, embedded images, foreign objects, animation or URL references.
func ValidateLogo(uri string) error {
	parts := strings.SplitN(uri, ",", 2)
	if len(parts) != 2 || len(uri) > 22000 {
		return ErrInvalid
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(raw) == 0 || len(raw) > 16<<10 || base64.StdEncoding.EncodeToString(raw) != parts[1] {
		return ErrInvalid
	}
	switch parts[0] {
	case "data:image/png;base64":
		config, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 1024 || config.Height > 1024 {
			return ErrInvalid
		}
		if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
			return ErrInvalid
		}
		return nil
	case "data:image/svg+xml;base64":
		if !utf8.Valid(raw) {
			return ErrInvalid
		}
		decoder := xml.NewDecoder(bytes.NewReader(raw))
		depth, roots := 0, 0
		elements := " svg g path rect circle ellipse line polyline polygon title desc "
		attributes := " viewBox width height x y x1 x2 y1 y2 cx cy r rx ry d points fill stroke stroke-width stroke-linecap stroke-linejoin fill-rule clip-rule opacity fill-opacity stroke-opacity transform id "
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return ErrInvalid
			}
			switch v := token.(type) {
			case xml.StartElement:
				if depth == 0 {
					roots++
					if roots != 1 || v.Name.Local != "svg" {
						return ErrInvalid
					}
				}
				depth++
				if depth > 16 || !strings.Contains(elements, " "+v.Name.Local+" ") || v.Name.Space != "" && v.Name.Space != "http://www.w3.org/2000/svg" {
					return ErrInvalid
				}
				for _, a := range v.Attr {
					if a.Name.Local == "xmlns" && a.Name.Space == "" && a.Value == "http://www.w3.org/2000/svg" {
						continue
					}
					if a.Name.Space != "" || !strings.Contains(attributes, " "+a.Name.Local+" ") || strings.ContainsAny(a.Value, "\\&<>") || strings.Contains(strings.ToLower(a.Value), "url") {
						return ErrInvalid
					}
				}
			case xml.EndElement:
				depth--
			case xml.CharData:
				if depth == 0 && strings.TrimSpace(string(v)) != "" {
					return ErrInvalid
				}
			case xml.Comment:
			default:
				return ErrInvalid
			}
		}
		if roots == 1 && depth == 0 {
			return nil
		}
	}
	return ErrInvalid
}
