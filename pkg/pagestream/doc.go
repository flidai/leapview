// Package pagestream provides a small framework for Gomponents-rendered MPA
// pages that open one long-lived Datastar SSE transport.
//
// Pagestream is intentionally opinionated: update streams carry Datastar signal
// patches only and remain open while a page is hidden. Element morphs, client
// identity, route dispatch, authorization, delivery policy, and domain-specific
// patch generation belong outside this package. Command responses may use the
// redirect helper so application code does not depend on Datastar directly.
package pagestream
