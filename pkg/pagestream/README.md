# Pagestream

Pagestream is LeapView's deliberately small, opinionated Datastar subset for
stream-first multi-page applications.

Its framework contract is narrow:

- render a Gomponents document that opens one caller-supplied, same-origin update stream;
- decode Datastar signals;
- emit signal patches and redirects, but never element morphs or scripts; and
- fan signal patches out through bounded per-stream mailboxes.

Routes, client identity, authorization, application commands, delivery policy,
domain signal shapes, and browser components remain owned by LeapView. Datastar
is an implementation detail of this package; application handlers should
depend on Pagestream instead of growing their own Datastar surface.

The document keeps its one update stream open while the page is hidden. This is
an intentional invariant for server-owned page state, not a configurable
product policy. Generation ordering, coalescing, tracing, signal history, and
inspector APIs are outside the framework. Do not add new Datastar capabilities
merely because Datastar provides them.

The broker never blocks publishers or silently drops an individual patch. Each
subscription has a configurable pending limit (256 by default); reaching it
closes that slow subscription so the browser reconnects and rebuilds from
server-owned state.
