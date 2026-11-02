package servertemplate

import (
	"aster/billing/pkg/pluginapi/serverv1/conformance"
	"testing"
)

func TestConformance(t *testing.T) { conformance.Test(t, conformance.Suite{Processor: New()}) }
