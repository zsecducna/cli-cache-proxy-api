package openai

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func defaultStoreForWebsocketPreviousResponseID(rawJSON []byte) []byte {
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) == "" {
		return rawJSON
	}
	if gjson.GetBytes(rawJSON, "store").Exists() {
		return rawJSON
	}
	updated, err := sjson.SetBytes(rawJSON, "store", false)
	if err != nil {
		return rawJSON
	}
	return updated
}
