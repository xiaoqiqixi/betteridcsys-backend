// Package controllerapi is the public, vendor-neutral billing-to-controller
// wire contract. It exposes no billing database or payment operations.
package controllerapi

import "aster/billing/internal/controllers"

const Protocol = controllers.Protocol

type ProductSpec = controllers.ProductSpec
type Resources = controllers.Resources
type Request = controllers.Request
type Result = controllers.Result
