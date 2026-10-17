package db

import (
	"path/filepath"
	"testing"
)

func TestAssetSearchLiteralMetadata(t *testing.T) {
	d, err := Open("file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "assets-search.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	store := d.Assets()
	id, err := store.UpsertHTTPService(UpsertHTTPServiceReq{
		URL:          "https://kb-search-literal.invalid:8443/api/orders",
		PageTitle:    "KBSearchFixture 100%_literal",
		Technologies: []string{"KBSearchUniqueTech"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id)
	for _, query := range []string{"KB-SEARCH-LITERAL.INVALID", "/api/orders", "8443", "kbsearchuniquetech", "100%_literal"} {
		rows, err := store.Search(query, 25)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row.ID == id {
				found = true
			}
		}
		if !found {
			t.Errorf("query %q did not match fixture", query)
		}
	}
	for _, query := range []string{"100%Xliteral", "' OR 1=1 --", "kb-search-definitely-absent.invalid"} {
		rows, err := store.Search(query, 25)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Errorf("query must be literal %q: got %d rows", query, len(rows))
		}
	}
	for _, limit := range []int{-1, 0, 1, 100, 1000} {
		rows, err := store.Search("  ", limit)
		if err != nil {
			t.Fatal(err)
		}
		bound := limit
		if bound <= 0 {
			bound = 25
		}
		if bound > 100 {
			bound = 100
		}
		if len(rows) > bound {
			t.Errorf("limit %d returned %d", limit, len(rows))
		}
	}
}
