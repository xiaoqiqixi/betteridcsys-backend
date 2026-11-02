package financeapi

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestConcurrentTaxonomyValidationAfterIdle(t *testing.T) {
	taxonomy := CatalogTaxonomy{Revision: 1,
		Types:   []CatalogType{{ID: "compute", Slug: "compute", Name: LocalizedText{"en": "Compute", "zh-CN": "计算资源"}, Icon: "server", SortOrder: -9007199254740991, Visible: true}},
		Regions: []CatalogRegion{{ID: "singapore", TypeID: "compute", Name: LocalizedText{"en": "Singapore", "zh-CN": "新加坡"}, Country: "SG", City: "Singapore", Visible: true}},
		Nodes:   []CatalogNode{{ID: "node1", RegionID: "singapore", Name: LocalizedText{"en": "Node", "zh-CN": "节点"}, NetworkLabel: "Global transit", TestIP: "203.0.113.10"}},
	}
	raw, err := json.Marshal(taxonomy)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateInput("adminUpdateTaxonomy", raw); err != nil {
		t.Fatal(err)
	}
	// regexp2 stops its timeout clock after idle time. A fresh valid request
	// must not inherit the expired deadline from before that clock stopped.
	time.Sleep(2 * time.Second)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Go(func() {
			for i := 0; i < 1000; i++ {
				if err := ValidateInput("adminUpdateTaxonomy", raw); err != nil {
					t.Errorf("validation %d: %v", i, err)
					return
				}
			}
		})
	}
	wg.Wait()
	taxonomy.Types[0].SortOrder = -9007199254740992
	raw, err = json.Marshal(taxonomy)
	if err != nil {
		t.Fatal(err)
	}
	if ValidateInput("adminUpdateTaxonomy", raw) == nil {
		t.Fatal("accepted an integer outside the contract's exact range")
	}
}
