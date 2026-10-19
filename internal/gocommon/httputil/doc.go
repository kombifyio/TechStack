// Package httputil holds the HTTP plumbing shared by kombify Go services:
// request-ID middleware, the standard API response envelope and baseline
// security headers.
//
// Consumer boundary: any kombify service may import it. It is transport
// helper code only, carries no authentication, identity or product policy,
// and must not grow any (use identity, edgeauth or servicecall for that).
//
// Stability: stable. Known consumers: kombify-Techstack and kombify-Simulate.
package httputil
