package main

import (
	"context"
	"net/http"
)

// This synthetic identifier denotes a fixed archived source, not a Kiwix catalog UUID.
const archivedWikiHowID = "00000000-0000-0000-0000-000000000001"

func resolveCollection(ctx context.Context, client *http.Client, base, id string) (downloadRequest, error) {
	if id == archivedWikiHowID {
		return downloadRequest{
			URL:   "https://archive.download.kiwix.org/zim/wikihow/wikihow_en_maxi_2023-03.zim",
			Name:  "wikihow_en_maxi_2023-03.zim",
			Bytes: 51256446526,
			// Local baseline from our existing copy, not a publisher checksum.
			SHA256:    "2345b7357c874b0d74b91e9c3a81e5a6c46b677a967e69f8beaca2e8a1d1fbf7",
			CatalogID: id,
		}, nil
	}
	return resolveCatalogItem(ctx, client, base, id)
}
